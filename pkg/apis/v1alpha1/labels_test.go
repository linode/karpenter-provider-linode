package v1alpha1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestTopologyZoneIsWellKnown(t *testing.T) {
	if !karpv1.WellKnownLabels.Has(corev1.LabelTopologyZone) {
		t.Fatal("topology zone must remain a well-known label for offering compatibility")
	}
}
