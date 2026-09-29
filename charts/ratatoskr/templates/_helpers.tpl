{{- define "ratatoskr.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "ratatoskr.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}{{ printf "%s-%s" .Release.Name (include "ratatoskr.name" .) | trunc 63 | trimSuffix "-" }}{{ end }}
{{- end }}
{{- define "ratatoskr.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
app.kubernetes.io/name: {{ include "ratatoskr.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "ratatoskr.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ratatoskr.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "ratatoskr.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}{{ default (include "ratatoskr.fullname" .) .Values.serviceAccount.name }}{{ else }}{{ required "serviceAccount.name is required when serviceAccount.create=false" .Values.serviceAccount.name }}{{ end }}
{{- end }}
{{- define "ratatoskr.image" -}}
{{- if .Values.image.digest }}{{ printf "%s@%s" .Values.image.repository .Values.image.digest }}{{ else }}{{ printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}{{ end }}
{{- end }}
