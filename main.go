package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"text/template"

	"gopkg.in/yaml.v2"
)

var deployment = flag.String("deployment", "", "Path to the kubernetes deployment file to manipulate")
var upstreams = flag.String("upstreams", "", "Space delimited string of upstream services to add. e.g: api:9090 web:9091")
var service = flag.String("service", "", "Name of the service to create in Consul")
var port = flag.String("port", "", "Port the service is exposed on")
var help = flag.Bool("help", false, "Usage instructions")

type data struct {
	Upstreams []upstream
	Service   string
	Port      string
}

type upstream struct {
	Service string
	Port    string
}

func newData() data {
	return data{
		Upstreams: []upstream{},
	}
}

func main() {
	flag.Parse()

	if *help {
		fmt.Println("Injects Consul service mesh containers to Kubernetes Deployments")
		fmt.Println("e.g. To inject containers for the service 'web' running on port '9090' with the upstream 'api:9091'")
		fmt.Println(`
consul-injection \
	--upstreams "api:9091" \
	--deployment ./example/web.yaml \
	--service web --port 9090 \
	> output.yaml
`)
		flag.Usage()
		os.Exit(0)
	}

	if *service == "" {
		fmt.Println("Please specify the service name")

		flag.Usage()
	}

	if *port == "" {
		fmt.Println("Please specify the port name")

		flag.Usage()
	}

	d := newData()
	d.Service = *service
	d.Port = *port

	if *upstreams != "" {
		services := strings.Split(*upstreams, " ")
		for _, s := range services {
			parts := strings.Split(s, ":")
			if len(parts) != 2 {
				fmt.Printf("Invalid upstream %s, upstreams should be formatted 'service:port'", s)
				os.Exit(1)
			}

			d.Upstreams = append(d.Upstreams, upstream{Service: parts[0], Port: parts[1]})
		}
	}

	sidecarTemplate := map[string]interface{}{}

	// read the template
	err := yaml.Unmarshal([]byte(sideCarContainers), &sidecarTemplate)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	newDeployment, err := appendToDeployment(
		*deployment,
		sidecarTemplate["containers"].([]interface{}),
		sidecarTemplate["initContainers"].([]interface{}),
		sidecarTemplate["volumes"].([]interface{}),
	)

	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	out, err := yaml.Marshal(newDeployment)

	// write the processed template
	tmpl, _ := template.New("test").Parse(string(out))
	tmpl.Execute(os.Stdout, d)
}

func appendToDeployment(path string, containers []interface{}, initContainers []interface{}, volumes []interface{}) (map[string]interface{}, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Unable to read deployment file %s: %s", path, err)
	}

	deployment := map[string]interface{}{}

	err = yaml.Unmarshal(data, &deployment)
	if err != nil {
		return nil, fmt.Errorf("Unable to parse deployment file %s: %s", path, err)
	}

	// get the containers
	spec, ok := deployment["spec"].(map[interface{}]interface{})
	if !ok {
		fmt.Println(deployment["spec"])
		return nil, fmt.Errorf("Unable to parse deployment file %s. Deployment does not contain a 'spec'", path)
	}

	template, ok := spec["template"].(map[interface{}]interface{})
	if !ok {
		return nil, fmt.Errorf("Unable to parse deployment file %s. Deployment does not contain a 'template'", path)
	}

	spec, ok = template["spec"].(map[interface{}]interface{})
	if !ok {
		return nil, fmt.Errorf("Unable to parse deployment file %s. Deployment does not contain a template 'spec'", path)
	}

	c, ok := spec["containers"].([]interface{})
	if !ok {
		fmt.Printf("%#v", spec["containers"])
		return nil, fmt.Errorf("Unable to parse deployment file %s. Deployment does not contain any 'containers'", path)
	}

	c = append(c, containers...)
	spec["containers"] = c

	c, ok = spec["initContainers"].([]interface{})
	if !ok {
		spec["initContainers"] = initContainers
	} else {
		c = append(c, initContainers...)
		spec["initContainers"] = c
	}

	c, ok = spec["volumes"].([]interface{})
	if !ok {
		spec["volumes"] = volumes
	} else {
		c = append(c, volumes...)
		spec["initContainers"] = c
	}

	return deployment, nil
}

var sideCarContainers = `
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
    value: consul-server.default.svc:8301
  - name: SERVICE_NAME
    value: "{{ .Service }}"
  - name: SERVICE_PORT
    value: "{{ .Port }}"
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
        curl http://127.0.0.1:8500/v1/status/leader \
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
  - mountPath: /consul/envoy
    name: consul-connect-envoy-data
- name: consul-connect-envoy-sidecar
  command:
  - /bin/sh
  - -ec
  - |
    /consul/bin/consul connect envoy \
    -proxy-id="${SERVICE_NAME}-sidecar-proxy-${POD_NAME}" \
    -bootstrap > /consul/envoy/envoy-bootstrap.yaml
    envoy \
    --config-path \
    /consul/envoy/envoy-bootstrap.yaml
  env:
  - name: CONSUL_HTTP_ADDR
    value: http://localhost:8500
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: SERVICE_NAME
    value: "{{ .Service }}"
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
initContainers:
- name: consul-init
  command:
  - /bin/sh
  - -ec
  - |
    # Create the service definition
    # the consul agent will automatically read this config and register the service
    # and de-register it on exit.
    
    cat <<EOF >/consul/config/service.hcl
    services {
      id   = "${SERVICE_NAME}-${POD_NAME}"
      name = "${SERVICE_NAME}"
      address = "${POD_IP}"
      port = ${SERVICE_PORT}
      tags = ["v1"]
      meta = {
        pod-name = "${POD_NAME}"
      }
    }
    services {
      id   = "${SERVICE_NAME}-sidecar-proxy-${POD_NAME}"
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
        destination_service_id = "${SERVICE_NAME}-${POD_NAME}"
        local_service_address = "127.0.0.1"
        local_service_port = ${SERVICE_PORT}
        {{ range .Upstreams }}
        upstreams {
          destination_type = "service"
          destination_name = "{{ .Service }}"
          local_bind_port = {{ .Port }}
        }
        {{ end }}
      }

      checks {
        name = "Proxy Public Listener"
        tcp = "${POD_IP}:20000"
        interval = "10s"
        deregister_critical_service_after = "10m"
      }

      checks {
        name = "Destination Alias"
        alias_service = "${SERVICE_NAME}-${POD_NAME}"
      }

    }
    EOF
    
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
  - name: SERVICE_PORT
    value: "{{ .Port }}"
  image: hashicorp/consul:1.9.1
  imagePullPolicy: IfNotPresent
  volumeMounts:
  - mountPath: /consul/config
    name: consul-connect-config-data
  - mountPath: /consul/bin
    name: consul-connect-bin-data
volumes:
- emptyDir: {}
  name: consul-connect-envoy-data
- emptyDir: {}
  name: consul-connect-config-data
- emptyDir: {}
  name: consul-connect-bin-data
- emptyDir: {}
  name: consul-agent-data
`