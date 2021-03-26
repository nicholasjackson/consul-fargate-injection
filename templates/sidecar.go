package templates

const SidecarContainers = `
---
containers:
- name: consul-agent
  command:
  - /bin/sh
  - -ec
  - |

    exec /bin/consul agent \
      -node="${HOSTNAME}" \
      -advertise="${POD_IP}" \
      -bind=0.0.0.0 \
      -client=0.0.0.0 \
      -hcl='leave_on_terminate = true' \
      -hcl='ports { grpc = 8502 }' \
      {{- if .TLSEnabled }}
      -hcl='ca_file = "/consul/tls/tls.crt"' \
      -hcl='auto_encrypt = {tls = true}' \
      -hcl="auto_encrypt = {ip_san = [\"$POD_IP\"]}" \
      -hcl='verify_outgoing = true' \
      -hcl='ports { https = 8501 }' \
      -hcl='ports { http = -1 }' \
      {{ end -}}
      -config-dir=/consul/config \
      -datacenter=dc1 \
      -data-dir=/consul/data \
      -retry-join="${CONSUL_SVC_ADDRESS}" \
      -domain=consul
  env:
  - name: POD_IP
    valueFrom:
      fieldRef:
        apiVersion: v1
        fieldPath: status.podIP
  - name: NAMESPACE
    valueFrom:
      fieldRef:
        apiVersion: v1
        fieldPath: metadata.namespace
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: CONSUL_SVC_ADDRESS
    value: "{{ .ConsulServer }}"
  - name: SERVICE_NAME
    value: "{{ .Service }}"
  - name: SERVICE_PORT
    value: "{{ .Port }}"
  {{- if .TLSEnabled }}
  - name: CONSUL_HTTP_SSL_VERIFY
    value: "false"
  {{end}}
  image: hashicorp/consul:1.9.1
  imagePullPolicy: IfNotPresent
  ports:
  - containerPort: 8500
    name: http
    protocol: TCP
  - containerPort: 8502
    name: grpc
    protocol: TCP
  - containerPort: 8301
    name: serflan-tcp
    protocol: TCP
  - containerPort: 8301
    name: serflan-udp
    protocol: UDP
  - containerPort: 8600
    name: dns-tcp
    protocol: TCP
  - containerPort: 8600
    name: dns-udp
    protocol: UDP
  readinessProbe:
    exec:
      command:
      - /bin/sh
      - -ec
      - |
        curl -k {{ .ConsulHTTPAddr }}/v1/status/leader \
        2>/dev/null | grep -E '".+"'
    failureThreshold: 3
    periodSeconds: 10
    successThreshold: 1
    timeoutSeconds: 1
  resources:
    limits:
      cpu: 100m
      memory: 100Mi
    requests:
      cpu: 100m
      memory: 100Mi
  terminationMessagePath: /dev/termination-log
  terminationMessagePolicy: File
  volumeMounts:
  - mountPath: /consul/data
    name: consul-agent-data
  - mountPath: /consul/config
    name: consul-connect-config-data
  {{- if .TLSEnabled }}
  - mountPath: "/consul/tls"
    name: consul-ca-cert
    readOnly: true
  {{ end }}
- name: consul-connect-envoy-sidecar
  command:
  - /bin/sh
  - -ec
  - |

    # Register the service
    /consul/bin/consul services register \
      {{if .ACLsEnabled}}-token-file="/consul/envoy/acl-token" \{{end -}}
      /consul/envoy/service.hcl
    
    # Generate the envoy config
    /consul/bin/consul connect envoy \
      -proxy-id="${PROXY_SERVICE_ID}" \
      {{if .ACLsEnabled}}-token-file="/consul/envoy/acl-token" \{{end -}}
      -bootstrap > /consul/envoy/envoy-bootstrap.yaml

    # Run the envoy sidecar
    envoy \
    --config-path \
    /consul/envoy/envoy-bootstrap.yaml
  lifecycle:
    preStop:
      exec:
        command:
        - /bin/sh
        - -ec
        - |-
          /consul/bin/consul services deregister \
          {{if .ACLsEnabled}}-token-file="/consul/envoy/acl-token" \{{end}}
          /consul/config/service.hcl
          
          {{if .ACLsEnabled }}
          /consul/bin/consul logout \
            -token-file="/consul/envoy/acl-token"
          {{end}}
  env:
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: PROXY_SERVICE_ID
    value: "$(POD_NAME)-{{ .Service }}-sidecar-proxy"
  - name: CONSUL_HTTP_ADDR
    value: {{ .ConsulHTTPAddr }}
  - name: CONSUL_GRPC_ADDR
    value: localhost:8502
  {{- if .TLSEnabled }}
  - name: CONSUL_HTTP_SSL_VERIFY
    value: "false"
  - name: CONSUL_CACERT
    value: "/consul/tls/tls.crt"
  {{end}}
  image: envoyproxy/envoy-alpine:v1.16.0
  imagePullPolicy: IfNotPresent
  resources: {}
  terminationMessagePath: /dev/termination-log
  terminationMessagePolicy: File
  volumeMounts:
  - mountPath: /consul/envoy
    name: consul-connect-envoy-data
  - mountPath: /consul/bin
    name: consul-connect-bin-data
  {{- if .TLSEnabled }}
  - mountPath: "/consul/tls"
    name: consul-tls-data
  {{ end }}
initContainers:
- name: consul-init
  command:
  - /bin/sh
  - -ec
  - |
    # Create the service definition
    # the consul agent will automatically read this config and register the service
    # and de-register it on exit.

    cat <<EOF >/consul/envoy/service.hcl
    services {
      id   = "${SERVICE_ID}"
      name = "${SERVICE_NAME}"
      address = "${POD_IP}"
      port = ${SERVICE_PORT}
      tags = ["v1"]
      meta = {
        pod-name = "${POD_NAME}"
      }
    }

    services {
      id   = "${PROXY_SERVICE_ID}"
      name = "${SERVICE_NAME}-sidecar-proxy"
      kind = "connect-proxy"
      address = "${POD_IP}"
      port = 20000
      tags = ["v1"]
      meta = {
        pod-name = "${POD_NAME}"
      }

      proxy {
        destination_service_name = "${SERVICE_NAME}"
        destination_service_id = "${SERVICE_ID}"
        local_service_address = "127.0.0.1"
        local_service_port = ${SERVICE_PORT}

        {{ range .Upstreams -}}
        upstreams {
          destination_type = "service"
          destination_name = "{{ .Service }}"
          local_bind_port = {{ .Port }}
        }
        {{end -}}

       }

      checks {
        name = "Proxy Public Listener"
        tcp = "${POD_IP}:20000"
        interval = "10s"
        deregister_critical_service_after = "10m"
      }

      checks {
        name = "Destination Alias"
        alias_service = "${SERVICE_ID}"
      }

    }
    EOF

    
    cat <<EOF >/consul/config/config.json
    {
      "check_update_interval": "0s",
      "enable_central_service_config": true
    }
    EOF
    
    # Get the Consul CA Cert
    {{if .TLSEnabled -}}
    curl ${CONSUL_HTTP_ADDR}/v1/connect/ca/roots?pem=true -k > /consul/tls/tls.crt
    {{- end}}
    
    {{if .ACLsEnabled -}}
    # Authenticate with Consul to obtain an ACL token
    /bin/consul login -method="consul-k8s-auth-method" \
      -bearer-token-file="/var/run/secrets/kubernetes.io/serviceaccount/token" \
      -token-sink-file="/consul/envoy/acl-token" \
      -meta="pod=${NAMESPACE}/${POD_NAME}"
    
    chmod 444 /consul/envoy/acl-token

    # Create the ACL config for the client
    cat << EOF > /consul/config/client_acl_config.json
    {
      "acl": {
        "enabled": true,
        "default_policy": "deny",
        "down_policy": "extend-cache",
        "tokens": {
          "agent": "${CLIENT_ACL_TOKEN}"
        }
      }
    }
    EOF
    {{- end}}

    # Copy the Consul binary
    cp /bin/consul /consul/bin/consul
  env:
  - name: POD_IP
    valueFrom:
      fieldRef:
        apiVersion: v1
        fieldPath: status.podIP
  - name: NAMESPACE
    valueFrom:
      fieldRef:
        apiVersion: v1
        fieldPath: metadata.namespace
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: SERVICE_NAME
    value: "{{ .Service }}"
  - name: SERVICE_ID
    value: "$(POD_NAME)-{{ .Service }}"
  - name: PROXY_SERVICE_ID
    value: "$(POD_NAME)-{{ .Service }}-sidecar-proxy"
  - name: SERVICE_PORT
    value: "{{ .Port }}"
  - name: CONSUL_HTTP_ADDR
    value: {{ .ConsulServerHTTPAddr }}
  {{if .ACLsEnabled}}
  - name: CLIENT_ACL_TOKEN
    valueFrom:
      secretKeyRef:
        name: {{ .ConsulClientACLSecret }}
        key: token
  {{end}}
  {{- if .TLSEnabled }}
  - name: CONSUL_HTTP_SSL_VERIFY
    value: "false"
  - name: CONSUL_CACERT
    value: "/consul/tls/tls.crt"
  {{end}}
  image: hashicorp/consul:1.9.1
  imagePullPolicy: IfNotPresent
  volumeMounts:
  - mountPath: /consul/envoy
    name: consul-connect-envoy-data
  - mountPath: /consul/config
    name: consul-connect-config-data
  - mountPath: /consul/bin
    name: consul-connect-bin-data
  - mountPath: /consul/tls
    name: consul-tls-data
volumes:
- emptyDir: {}
  name: consul-connect-envoy-data
- emptyDir: {}
  name: consul-connect-config-data
- emptyDir: {}
  name: consul-connect-bin-data
- emptyDir: {}
  name: consul-agent-data
- emptyDir: {}
  name: consul-tls-data
- name: consul-ca-cert
  secret:
    secretName: "{{.ConsulServerCASecret}}"
    items:
    - key: tls.crt
      path: tls.crt
`
