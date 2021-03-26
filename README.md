# EKS Fargate Consul Sidecar Injector

Consul Service Mesh for Kubernetes automatically injects the sidecar containers for the service mesh data plane when it detects the presence
of the annotation `'consul.hashicorp.com/connect-inject': 'true'`

```yaml
metadata:
  name: static-server
  annotations:
    'consul.hashicorp.com/connect-inject': 'true'
```

The proxies (data plane) injected by the Consul communicate with the Consul server via an intermediary agent called a Consul client, this allows
local caching of data and distributed load for the control plane. 

In a typical setup these agents run in a Kubernetes Daemonset, however Daemonsets are not currently supported by EKS Fargate at present and
this stops the injection process from functioning correctly. 

![](./images/consul_1.png)

The HashiCorp Consul team is aware of this limitation with Fargate and is working on an official fix to change the function of the proxy injector
for EKS Fargate. Until this has been completed this repo contains a simple utility that appends the necessary containers and volumes to your Deployments
allowing them to participate in the service mesh.  

![](./images/consul_2.png)

**NOTE: The current release does not support ACL Tokens**

## Usage

```
Injects Consul service mesh containers to Kubernetes Deployments
e.g. To inject containers for the service 'web' running on port '9090' with the upstream 'api:9091'

consul-injection \
        --upstreams "api:9091" \
        --deployment ./example/web.yaml \
        --service web --port 9090 \
        > output.yaml

Usage of /tmp/go-build192523820/b001/exe/main:
  -acl-enabled
        ACLs are enabled for the server, setting this option will enable consul login using the service account token
  -client-secret string
        Consul client ACL token to be used for service registration (default "consul-client-acl-token")
  -consul-server string
        Address of the Consul server (default "consul-server.default.svc")
  -deployment string
        Path to the kubernetes deployment file to manipulate
  -help
        Usage instructions
  -port string
        Port the service is exposed on
  -server-ca-secret string
        Secret containing the Consul server root cert (default "consul-ca-cert")
  -service string
        Name of the service to create in Consul
  -tls-enabled
        TLS is enabled for the server, setting this option will configure the consul agent using autoencrypt
  -upstreams string
        Space delimited string of upstream services to add. e.g: api:9090 web:9091
```

Given the standard Kubernetes deployment `web.yaml`:

```yaml
---
apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    app: web
  name: web
spec:
  replicas: 1
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
        metrics: enabled
    spec:
      containers:
      - env:
        - name: LISTEN_ADDR
          value: 127.0.0.1:9090
        - name: NAME
          value: web
        - name: MESSAGE
          value: Response from Web
        - name: UPSTREAM_URIS
          value: http://localhost:9091
        image: nicholasjackson/fake-service:v0.20.0
        name: web
        ports:
        - containerPort: 9090
```

Running the following command would inject the necessary containers to allow this deployment to be part of the service mesh
and provides access to the upstream service `api` at `localhost:9090`.

```shell
consul-injection \
        --upstreams "api:9091" \
        --deployment ./example/web.yaml \
        --service web --port 9090 \
        > output.yaml
```

The transformed deployment will look like:

```yaml
---
apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    app: web
  name: web
spec:
  replicas: 1
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
        metrics: enabled
    spec:
      containers:
      - env:
        - name: LISTEN_ADDR
          value: 127.0.0.1:9090
        - name: NAME
          value: web
        - name: MESSAGE
          value: Response from Web
        - name: UPSTREAM_URIS
          value: http://localhost:9091
        image: nicholasjackson/fake-service:v0.20.0
        name: web
        ports:
        - containerPort: 9090
      - command:
        - /bin/sh
        - -ec
        - |2

          exec /bin/consul agent \
            -node="${HOSTNAME}" \
            -advertise="${POD_IP}" \
            -bind=0.0.0.0 \
            -client=0.0.0.0 \
            -hcl='leave_on_terminate = true' \
            -hcl='ports { grpc = 8502 }' \-config-dir=/consul/config \
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
          value: consul-server.default.svc:8301
        - name: SERVICE_NAME
          value: web
        - name: SERVICE_PORT
          value: "9090"
        image: hashicorp/consul:1.9.1
        imagePullPolicy: IfNotPresent
        name: consul-agent
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
              curl -k http://localhost:8500/v1/status/leader \
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
      - command:
        - /bin/sh
        - -ec
        - |2

          # Register the service
          /consul/bin/consul services register \
            /consul/envoy/service.hcl

          # Generate the envoy config
          /consul/bin/consul connect envoy \
            -proxy-id="${PROXY_SERVICE_ID}" \
            -bootstrap > /consul/envoy/envoy-bootstrap.yaml

          # Run the envoy sidecar
          envoy \
          --config-path \
          /consul/envoy/envoy-bootstrap.yaml
        env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: PROXY_SERVICE_ID
          value: $(POD_NAME)-web-sidecar-proxy
        - name: CONSUL_HTTP_ADDR
          value: http://localhost:8500
        - name: CONSUL_GRPC_ADDR
          value: localhost:8502
        image: envoyproxy/envoy-alpine:v1.16.0
        imagePullPolicy: IfNotPresent
        lifecycle:
          preStop:
            exec:
              command:
              - /bin/sh
              - -ec
              - |-
                /consul/bin/consul services deregister \

                /consul/config/service.hcl
        name: consul-connect-envoy-sidecar
        resources: {}
        terminationMessagePath: /dev/termination-log
        terminationMessagePolicy: File
        volumeMounts:
        - mountPath: /consul/envoy
          name: consul-connect-envoy-data
        - mountPath: /consul/bin
          name: consul-connect-bin-data
      initContainers:
      - command:
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

              upstreams {
                destination_type = "service"
                destination_name = "api"
                local_bind_port = 9091
              }
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
          value: web
        - name: SERVICE_ID
          value: $(POD_NAME)-web
        - name: PROXY_SERVICE_ID
          value: $(POD_NAME)-web-sidecar-proxy
        - name: SERVICE_PORT
          value: "9090"
        - name: CONSUL_HTTP_ADDR
          value: http://consul-server.default.svc:8500
        image: hashicorp/consul:1.9.1
        imagePullPolicy: IfNotPresent
        name: consul-init
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
          items:
          - key: tls.crt
            path: tls.crt
          secretName: consul-ca-cert


```
