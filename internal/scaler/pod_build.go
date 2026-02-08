package scaler

import (
	"fmt"

	"github.com/coolguy1771/rssc/internal/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func (k *KubernetesScaler) buildPodLabels(runnerName string) map[string]string {
	podLabels := map[string]string{
		constants.LabelRunnerName:   runnerName,
		constants.LabelRunnerState:  constants.RunnerStateIdle,
		constants.LabelAutoscaleSet: fmt.Sprintf("%d", k.scaleSetID),
	}
	if k.runnerGroup != "" {
		podLabels[constants.LabelRunnerGroup] = k.runnerGroup
	}
	if k.template == nil || len(k.template.Metadata.Labels) == 0 {
		return podLabels
	}
	reservedLabels := map[string]bool{
		constants.LabelRunnerName: true, constants.LabelRunnerState: true,
		constants.LabelAutoscaleSet: true, constants.LabelRunnerGroup: true,
	}
	for labelKey, labelValue := range k.template.Metadata.Labels {
		if reservedLabels[labelKey] {
			k.logger.Info("Skipping reserved label from template", "label", labelKey, "reason", "reserved for system use")
			continue
		}
		podLabels[labelKey] = labelValue
	}
	return podLabels
}

func (k *KubernetesScaler) buildPodAnnotations(jitConfigEncoded string) map[string]string {
	podAnnotations := map[string]string{constants.AnnotationJITConfig: jitConfigEncoded}
	if k.template != nil && len(k.template.Metadata.Annotations) > 0 {
		for annKey, annVal := range k.template.Metadata.Annotations {
			podAnnotations[annKey] = annVal
		}
	}
	return podAnnotations
}

func (k *KubernetesScaler) buildEnvVars(jitConfigEncoded string) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{Name: "ACTIONS_RUNNER_INPUT_JITCONFIG", Value: jitConfigEncoded},
	}
	if k.template != nil && k.template.Spec.Env != nil {
		for _, env := range k.template.Spec.Env {
			envVars = append(envVars, corev1.EnvVar{Name: env.Name, Value: env.Value})
		}
	}
	return envVars
}

func (k *KubernetesScaler) buildRunnerPodSpec(envVars []corev1.EnvVar) corev1.PodSpec {
	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{
			{
				Name:    "runner",
				Image:   k.runnerImage,
				Command: []string{"/home/runner/run.sh"},
				Env:     envVars,
			},
		},
	}
	if k.template != nil && k.template.Spec.Resources.Limits != nil {
		limits := make(corev1.ResourceList)
		for resKey, resVal := range k.template.Spec.Resources.Limits {
			if qty, err := resource.ParseQuantity(resVal); err == nil {
				limits[corev1.ResourceName(resKey)] = qty
			}
		}
		if len(limits) > 0 {
			podSpec.Containers[0].Resources.Limits = limits
		}
	}
	if k.template != nil && k.template.Spec.Resources.Requests != nil {
		requests := make(corev1.ResourceList)
		for resKey, resVal := range k.template.Spec.Resources.Requests {
			if qty, err := resource.ParseQuantity(resVal); err == nil {
				requests[corev1.ResourceName(resKey)] = qty
			}
		}
		if len(requests) > 0 {
			podSpec.Containers[0].Resources.Requests = requests
		}
	}
	if k.template != nil && len(k.template.Spec.NodeSelector) > 0 {
		podSpec.NodeSelector = k.template.Spec.NodeSelector
	}
	if k.template != nil && len(k.template.Spec.Tolerations) > 0 {
		tolerations := make([]corev1.Toleration, 0, len(k.template.Spec.Tolerations))
		for _, t := range k.template.Spec.Tolerations {
			op := corev1.TolerationOperator(t.Operator)
			if op == "" {
				op = corev1.TolerationOpEqual
			}
			tolerations = append(tolerations, corev1.Toleration{
				Key: t.Key, Operator: op, Value: t.Value, Effect: corev1.TaintEffect(t.Effect),
			})
		}
		podSpec.Tolerations = tolerations
	}
	uid, gid := int64(1001), int64(1001)
	podSpec.SecurityContext = &corev1.PodSecurityContext{
		RunAsUser:      &uid,
		RunAsGroup:     &gid,
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	allowEscalation := false
	readOnlyRoot := false
	podSpec.Containers[0].SecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: &allowEscalation,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		ReadOnlyRootFilesystem:   &readOnlyRoot,
	}
	return podSpec
}
