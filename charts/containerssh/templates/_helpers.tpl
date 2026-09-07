{{- /*
Helpers for the ContainerSSH chart.
*/}}

{{- define "containerssh.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "containerssh.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "containerssh.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "containerssh.labels" -}}
helm.sh/chart: {{ include "containerssh.chart" . }}
{{ include "containerssh.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "containerssh.selectorLabels" -}}
app.kubernetes.io/name: {{ include "containerssh.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "containerssh.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "containerssh.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "containerssh.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | toString) }}
{{- end }}

{{/*
Whether an SSH host key is provided, and which secret to mount it from.
Returns "true"/"false" when an existing secret or a chart-rendered secret is used.
*/}}
{{- define "containerssh.hostKeySecret" -}}
{{- if .Values.ssh.hostKey.existingSecret -}}
{{- .Values.ssh.hostKey.existingSecret }}
{{- else if .Values.ssh.hostKey.privateKey -}}
{{- include "containerssh.fullname" . }}-hostkey
{{- end -}}
{{- end }}
