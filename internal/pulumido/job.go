/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pulumido

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/dirien/doplane/internal/runnerops"
)

const (
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelVerb      = "do.pulumi.com/verb"
	operatorName   = "doplane"

	// bakedPluginsDir is where the runner image keeps its pre-installed
	// plugins; the doplane-runner binary seeds them into each workspace.
	bakedPluginsDir = "/opt/pulumi-home/plugins"
)

// ownerCtxKey carries the identity of the object an operation is performed
// for, so runner Jobs get deterministic, adoptable names.
type ownerCtxKey struct{}

// WithOwner tags ctx with the owning object's identity (e.g.
// "namespace/name"). Runner Jobs for the same owner and operation reuse the
// same Job name, so an interrupted reconcile adopts the in-flight Job on
// retry instead of re-running the cloud mutation.
func WithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerCtxKey{}, owner)
}

func ownerFromContext(ctx context.Context) string {
	owner, _ := ctx.Value(ownerCtxKey{}).(string)
	return owner
}

// credentialsCtxKey carries a provider-specific credentials Secret name
// resolved from a DoProvider profile; it overrides the JobRunner's default
// CredentialsSecret for the tagged operation.
type credentialsCtxKey struct{}

// WithCredentialsSecret tags ctx with the credentials Secret runner Jobs of
// this operation load their environment from (resolved in the Job's
// namespace).
func WithCredentialsSecret(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, credentialsCtxKey{}, name)
}

func credentialsFromContext(ctx context.Context) string {
	name, _ := ctx.Value(credentialsCtxKey{}).(string)
	return name
}

// namespaceCtxKey carries the namespace of the object an operation is
// performed for, so a per-resource-namespace JobRunner can execute the Job —
// and resolve its credentials Secret — in the tenant's namespace.
type namespaceCtxKey struct{}

// WithNamespace tags ctx with the owning object's namespace. A JobRunner in
// per-resource-namespace mode runs the operation's Job there, picking up the
// credentials Secret of that namespace (per-tenant credentials).
func WithNamespace(ctx context.Context, namespace string) context.Context {
	return context.WithValue(ctx, namespaceCtxKey{}, namespace)
}

func namespaceFromContext(ctx context.Context) string {
	ns, _ := ctx.Value(namespaceCtxKey{}).(string)
	return ns
}

// JobRunner ships every operation to a dedicated, hardened Kubernetes Job
// running the doplane-runner binary, so provider plugins execute in their own
// container with their own resource limits and credentials, isolated from
// the controller manager. The operation travels as one JSON document; the
// outcome comes back as one typed envelope in the pod log.
type JobRunner struct {
	Clientset kubernetes.Interface
	// Namespace is where runner Jobs are created (the operator namespace).
	Namespace string
	// PerResourceNamespace runs each Job in the owning object's namespace
	// (from WithNamespace) instead of Namespace. The credentials Secret is
	// then resolved in the tenant's namespace, isolating cloud credentials
	// per namespace. Operations without a namespace tag (e.g. shared schema
	// fetches) fall back to Namespace, and teardown operations fall back to
	// Namespace when the tenant namespace is already terminating (Kubernetes
	// rejects new Jobs there) so finalizers cannot wedge.
	PerResourceNamespace bool
	// Image is the runner image containing the doplane-runner binary, the
	// pulumi CLI, language toolchains and baked provider plugins.
	Image string
	// CredentialsSecret is an optional Secret whose keys become environment
	// variables of the runner (cloud provider credentials, registry token).
	// Resolved in the namespace the Job runs in.
	CredentialsSecret string
	// PluginCachePVC is an optional PersistentVolumeClaim holding the shared
	// writable plugin cache. When set, runner pods mount it at
	// PluginCacheMountPath and install pinned provider plugins there on
	// first use — new providers need no runner image rebuild.
	PluginCachePVC string
	// PluginCacheMountPath is where the cache PVC is mounted in runner pods.
	PluginCacheMountPath string
	// Timeout bounds a single operation end to end.
	Timeout time.Duration
}

var _ Runner = (*JobRunner)(nil)

func (r *JobRunner) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 10 * time.Minute
}

// Create implements Runner.
func (r *JobRunner) Create(ctx context.Context, token, pkg string, props map[string]any) (string, map[string]any, error) {
	res, err := r.executeOp(ctx, runnerops.Op{Verb: runnerops.VerbCreate, Token: token, Package: pkg, Properties: props})
	if err != nil {
		return "", nil, err
	}
	return res.ID, res.State, nil
}

// Patch implements Runner.
func (r *JobRunner) Patch(ctx context.Context, token, pkg, id string, props map[string]any) (map[string]any, error) {
	res, err := r.executeOp(ctx, runnerops.Op{Verb: runnerops.VerbPatch, Token: token, Package: pkg, ID: id, Properties: props})
	if err != nil {
		return nil, err
	}
	return res.State, nil
}

// Read implements Runner.
func (r *JobRunner) Read(ctx context.Context, token, pkg, id string) (map[string]any, error) {
	res, err := r.executeOp(ctx, runnerops.Op{Verb: runnerops.VerbRead, Token: token, Package: pkg, ID: id})
	if err != nil {
		return nil, err
	}
	return res.State, nil
}

// Delete implements Runner.
func (r *JobRunner) Delete(ctx context.Context, token, pkg, id string) error {
	_, err := r.executeOp(ctx, runnerops.Op{Verb: runnerops.VerbDelete, Token: token, Package: pkg, ID: id})
	return err
}

// FetchSchema implements Runner.
func (r *JobRunner) FetchSchema(ctx context.Context, pkg, token string) (*PackageSchema, error) {
	res, err := r.executeOp(ctx, runnerops.Op{Verb: runnerops.VerbSchema, Token: token, Package: pkg})
	if err != nil {
		return nil, err
	}
	var s PackageSchema
	if err := json.Unmarshal(res.Schema, &s); err != nil {
		return nil, fmt.Errorf("decoding schema for %s: %w", pkg, err)
	}
	return &s, nil
}

// CreateComponent implements Runner.
func (r *JobRunner) CreateComponent(ctx context.Context, token, pkg string, props map[string]any) (string, map[string]any, []byte, error) {
	res, err := r.executeOp(ctx, runnerops.Op{Verb: runnerops.VerbEngineUp, Token: token, Package: pkg, Properties: props})
	if err != nil {
		return "", nil, nil, err
	}
	return res.ID, res.Outputs, res.EngineState, nil
}

// UpdateComponent implements Runner.
func (r *JobRunner) UpdateComponent(ctx context.Context, token, pkg string, props map[string]any, engineState []byte) (map[string]any, []byte, error) {
	state, err := engineStateJSON(engineState)
	if err != nil {
		return nil, nil, err
	}
	res, err := r.executeOp(ctx, runnerops.Op{Verb: runnerops.VerbEngineUp, Token: token, Package: pkg, Properties: props, EngineState: state})
	if err != nil {
		return nil, nil, err
	}
	return res.Outputs, res.EngineState, nil
}

// DeleteComponent implements Runner.
func (r *JobRunner) DeleteComponent(ctx context.Context, token, pkg string, engineState []byte) error {
	state, err := engineStateJSON(engineState)
	if err != nil {
		return err
	}
	_, err = r.executeOp(ctx, runnerops.Op{Verb: runnerops.VerbEngineDestroy, Token: token, Package: pkg, EngineState: state})
	return err
}

// jobNamespace picks where an operation's Job runs: the owning object's
// namespace in per-resource mode (tenant credentials live there), the
// operator namespace otherwise.
func (r *JobRunner) jobNamespace(ctx context.Context) string {
	if r.PerResourceNamespace {
		if ns := namespaceFromContext(ctx); ns != "" {
			return ns
		}
	}
	return r.Namespace
}

// teardownVerb reports whether an operation removes the external resource —
// the operations that must still run while the owning namespace is being
// deleted, or its finalizers can never clear.
func teardownVerb(verb string) bool {
	return verb == runnerops.VerbDelete || verb == runnerops.VerbEngineDestroy
}

// ensureJob creates the operation's Job in namespace (adopting the Job left
// by a previous, interrupted attempt of this exact operation) and returns
// the namespace the Job actually lives in. A terminating tenant namespace
// rejects new Jobs; teardown operations then fall back to the operator
// namespace — and its credentials Secret, since the tenant's Secret is
// being deleted too — so DoResource finalizers cannot wedge the namespace.
func (r *JobRunner) ensureJob(ctx context.Context, namespace, name, verb, opJSON string) (string, error) {
	credentials := r.CredentialsSecret
	if fromProvider := credentialsFromContext(ctx); fromProvider != "" {
		// A DoProvider profile pinned this operation to its own Secret.
		credentials = fromProvider
	}
	job := r.buildJob(name, namespace, credentials, verb, opJSON)
	_, err := r.Clientset.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
	switch {
	case err == nil || apierrors.IsAlreadyExists(err):
		return namespace, nil
	case namespace != r.Namespace && teardownVerb(verb) && apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause):
		fallback := r.buildJob(name, r.Namespace, credentials, verb, opJSON)
		if _, err := r.Clientset.BatchV1().Jobs(r.Namespace).Create(ctx, fallback, metav1.CreateOptions{}); err != nil &&
			!apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("creating fallback runner job %s/%s for terminating namespace %s: %w",
				r.Namespace, name, namespace, err)
		}
		return r.Namespace, nil
	default:
		return "", fmt.Errorf("creating runner job %s/%s: %w", namespace, name, err)
	}
}

// jobName derives a deterministic name from the owning object and the exact
// operation document, so a retried reconcile adopts the previous attempt's
// Job instead of re-running the cloud mutation. Operations without an owner
// tag fall back to random names — except schema fetches, which are safely
// shareable because they are read-only and fully determined by the op.
func (r *JobRunner) jobName(ctx context.Context, verb, opJSON string) string {
	owner := ownerFromContext(ctx)
	if owner == "" && verb != runnerops.VerbSchema {
		return fmt.Sprintf("do-%s-%s", verb, rand.String(8))
	}
	h := sha256.New()
	_, _ = h.Write([]byte(owner))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(opJSON))
	return fmt.Sprintf("do-%s-%s", verb, hex.EncodeToString(h.Sum(nil))[:16])
}

// executeOp ensures a Job for the operation exists (creating or adopting
// it), waits for it to finish and decodes the result envelope. The Job is
// deleted only once a terminal outcome has been observed and its output
// secured; otherwise it is left in place so a later reconcile can adopt it.
func (r *JobRunner) executeOp(ctx context.Context, op runnerops.Op) (runnerops.Result, error) {
	opJSON, err := json.Marshal(op)
	if err != nil {
		return runnerops.Result{}, err
	}
	name := r.jobName(ctx, op.Verb, string(opJSON))

	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()

	namespace, err := r.ensureJob(ctx, r.jobNamespace(ctx), name, op.Verb, string(opJSON))
	if err != nil {
		return runnerops.Result{}, err
	}
	cleanup := false
	defer func() {
		if !cleanup {
			return // leave the Job for adoption by the next attempt
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cleanupCancel()
		_ = r.Clientset.BatchV1().Jobs(namespace).Delete(cleanupCtx, name, metav1.DeleteOptions{
			PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
		})
	}()

	failed := false
	var failMsg string
	err = wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		j, err := r.Clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, fmt.Errorf("runner job %s disappeared", name)
			}
			return false, nil // transient; keep polling
		}
		for _, c := range j.Status.Conditions {
			if c.Status != corev1.ConditionTrue {
				continue
			}
			switch c.Type {
			case batchv1.JobComplete:
				return true, nil
			case batchv1.JobFailed:
				failed = true
				failMsg = c.Message
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return runnerops.Result{}, fmt.Errorf("waiting for runner job %s (%s): %w", name, op.Verb, err)
	}

	logs, logsErr := r.podLogs(namespace, name)
	if failed {
		// The doplane-runner binary exits non-zero only for infrastructure
		// problems (op failures travel in the envelope), so a failed Job is
		// an infra failure. Classify only as a safety net.
		cleanup = true
		base := fmt.Errorf("runner job %s (%s) failed: %s: %s", name, op.Verb, failMsg,
			runnerops.Truncate(strings.TrimSpace(logs), 4000))
		if sentinel := classifyInfraFailure(logs, failMsg); sentinel != nil {
			return runnerops.Result{}, fmt.Errorf("%w: %w", sentinel, base)
		}
		return runnerops.Result{}, base
	}
	if logsErr != nil {
		// The operation succeeded but its result is unreadable right now.
		// Keep the Job (TTL is the backstop) so the retry re-reads the same
		// result instead of re-running the mutation.
		return runnerops.Result{}, fmt.Errorf("%w: runner job %s (%s) completed but its logs could not be read: %w",
			ErrOutputUnavailable, name, op.Verb, logsErr)
	}
	cleanup = true
	res, err := decodeEnvelope(logs)
	if err != nil {
		return runnerops.Result{}, err
	}
	return res, resultErr(res)
}

// podLogs fetches the logs of the Job's most recent pod, retrying transient
// API failures: for a completed Job these logs are the only copy of the
// operation result.
func (r *JobRunner) podLogs(namespace, jobName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", errors.Join(lastErr, ctx.Err())
			case <-time.After(2 * time.Second):
			}
		}
		pods, err := r.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "job-name=" + jobName,
		})
		if err != nil {
			lastErr = err
			continue
		}
		if len(pods.Items) == 0 {
			lastErr = fmt.Errorf("no pods found for job %s", jobName)
			continue
		}
		sort.Slice(pods.Items, func(i, j int) bool {
			return pods.Items[i].CreationTimestamp.Before(&pods.Items[j].CreationTimestamp)
		})
		pod := pods.Items[len(pods.Items)-1]
		raw, err := r.Clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{}).Do(ctx).Raw()
		if err != nil {
			lastErr = err
			continue
		}
		return string(raw), nil
	}
	return "", lastErr
}

func (r *JobRunner) buildJob(name, namespace, credentialsSecret, verb, opJSON string) *batchv1.Job {
	env := []corev1.EnvVar{
		{Name: "DOPLANE_OP", Value: opJSON},
		{Name: "DOPLANE_BAKED_PLUGINS", Value: bakedPluginsDir},
		{Name: "PULUMI_SKIP_UPDATE_CHECK", Value: "true"},
	}
	var envFrom []corev1.EnvFromSource
	if credentialsSecret != "" {
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: credentialsSecret},
				Optional:             ptr.To(true),
			},
		})
	}
	volumes := []corev1.Volume{{
		Name:         "tmp",
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}}
	mounts := []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}}
	var fsGroup *int64
	// PVC references are namespace-local: the cache claim lives in the
	// operator namespace, so per-resource-namespace Jobs skip it and fall
	// back to baked plugins plus on-demand downloads.
	if r.PluginCachePVC != "" && namespace == r.Namespace {
		env = append(env, corev1.EnvVar{Name: runnerops.EnvPluginCache, Value: r.PluginCacheMountPath})
		volumes = append(volumes, corev1.Volume{
			Name: "plugin-cache",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: r.PluginCachePVC},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: "plugin-cache", MountPath: r.PluginCacheMountPath})
		// The non-root runner (uid/gid 65532) must be able to write the
		// cache volume; the root filesystem stays read-only.
		fsGroup = ptr.To(int64(65532))
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				labelManagedBy: operatorName,
				labelVerb:      verb,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To(int32(0)),
			// Backstop cleanup in case the manager dies before deleting.
			TTLSecondsAfterFinished: ptr.To(int32(600)),
			ActiveDeadlineSeconds:   ptr.To(int64(r.timeout() / time.Second)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{labelManagedBy: operatorName},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					// Runner pods execute third-party provider plugins and
					// have no business talking to the Kubernetes API.
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr.To(true),
						RunAsUser:      ptr.To(int64(65532)),
						RunAsGroup:     ptr.To(int64(65532)),
						FSGroup:        fsGroup,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:    "doplane",
						Image:   r.Image,
						Command: []string{"/doplane-runner"},
						Env:     env,
						EnvFrom: envFrom,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							ReadOnlyRootFilesystem:   ptr.To(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: mounts,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					}},
					Volumes: volumes,
				},
			},
		},
	}
}
