{{- define "track.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "track.fullname" -}}
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

{{- define "track.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "track.labels" -}}
helm.sh/chart: {{ include "track.chart" . }}
{{ include "track.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: talyvor
{{- end }}

{{- define "track.selectorLabels" -}}
app.kubernetes.io/name: {{ include "track.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "track.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end }}

{{- define "track.secretName" -}}
{{- if .Values.secret.existingSecret -}}
{{- .Values.secret.existingSecret -}}
{{- else -}}
{{- printf "%s-env" (include "track.fullname" .) -}}
{{- end -}}
{{- end }}

{{- define "track.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "track.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end }}

{{/*
Env shared by the server and the migrate init container: the listen address,
the operator's plain settings, then the Secret.
*/}}
{{- define "track.env" -}}
env:
  - name: TRACK_LISTEN_ADDR
    value: {{ printf ":%v" .Values.containerPort | quote }}
  {{- range $k, $v := .Values.env }}
  - name: {{ $k }}
    value: {{ $v | quote }}
  {{- end }}
envFrom:
  - secretRef:
      name: {{ include "track.secretName" . }}
{{- end }}
