{{- define "olm-catalog-datasource.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "olm-catalog-datasource.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}{{ printf "%s-%s" .Release.Name (include "olm-catalog-datasource.name" .) | trunc 63 | trimSuffix "-" }}{{ end }}
{{- end }}
{{- define "olm-catalog-datasource.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
app.kubernetes.io/name: {{ include "olm-catalog-datasource.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "olm-catalog-datasource.selectorLabels" -}}
app.kubernetes.io/name: {{ include "olm-catalog-datasource.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "olm-catalog-datasource.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}{{ default (include "olm-catalog-datasource.fullname" .) .Values.serviceAccount.name }}{{ else }}{{ required "serviceAccount.name is required when serviceAccount.create=false" .Values.serviceAccount.name }}{{ end }}
{{- end }}
{{- define "olm-catalog-datasource.image" -}}
{{- if .Values.image.digest }}{{ printf "%s@%s" .Values.image.repository .Values.image.digest }}{{ else }}{{ printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}{{ end }}
{{- end }}
