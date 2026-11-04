#!/bin/bash
# Adds the (Helm templated) conversion webhook config to the chart's LinodeNodeClass CRD.
# controller-gen cannot generate spec.conversion, so this runs after the CRDs are copied into the chart.
# The CA bundle is intentionally absent: the Karpenter controller injects it at runtime.
set -eu -o pipefail

crd="${1:-charts/karpenter-crd/templates/karpenter.k8s.linode_linodenodeclasses.yaml}"

if grep -q "^  conversion:$" "$crd"; then
  exit 0
fi

awk '
  { print }
  /^spec:$/ {
    print "  {{- if .Values.webhook.enabled }}"
    print "  conversion:"
    print "    strategy: Webhook"
    print "    webhook:"
    print "      conversionReviewVersions:"
    print "        - v1"
    print "      clientConfig:"
    print "        service:"
    print "          name: {{ .Values.webhook.serviceName }}"
    print "          namespace: {{ .Values.webhook.serviceNamespace | default .Release.Namespace }}"
    print "          path: /convert"
    print "          port: {{ .Values.webhook.port }}"
    print "  {{- end }}"
  }
' "$crd" >"$crd.new" && mv "$crd.new" "$crd"
