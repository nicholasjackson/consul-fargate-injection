package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"text/template"

	"github.com/nicholasjackson/consul-sidecar-injection/templates"
	"gopkg.in/yaml.v2"
)

var deployment = flag.String("deployment", "", "Path to the kubernetes deployment file to manipulate")
var upstreams = flag.String("upstreams", "", "Space delimited string of upstream services to add. e.g: api:9090 web:9091")
var service = flag.String("service", "", "Name of the service to create in Consul")
var port = flag.String("port", "", "Port the service is exposed on")
var aclEnabled = flag.Bool("acl-enabled", false, "ACLs are enabled for the server, setting this option will enable consul login using the service account token")
var tlsEnabled = flag.Bool("tls-enabled", false, "TLS is enabled for the server, setting this option will configure the consul agent using autoencrypt")

var consulServer = flag.String("consul-server", "consul-server.default.svc", "Address of the Consul server")
var consulServerCASecret = flag.String("server-ca-secret", "consul-ca-cert", "Secret containing the Consul server root cert")
var consulClientTokenSecret = flag.String("client-secret", "consul-client-acl-token", "Consul client ACL token to be used for service registration")

var help = flag.Bool("help", false, "Usage instructions")

type data struct {
	Upstreams             []upstream
	Service               string
	Port                  string
	ACLsEnabled           bool
	TLSEnabled            bool
	ConsulHTTPAddr        string
	ConsulServer          string
	ConsulServerHTTPAddr  string
	ConsulClientACLSecret string
	ConsulServerCASecret  string
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
	> output.yaml`)

		fmt.Println("")
		flag.Usage()
		os.Exit(0)
	}

	if *service == "" {
		fmt.Println("Please specify the service name")

		flag.Usage()
		os.Exit(1)
	}

	if *port == "" {
		fmt.Println("Please specify the port name")

		flag.Usage()
		os.Exit(1)
	}

	d := newData()
	d.Service = *service
	d.Port = *port
	d.ConsulClientACLSecret = *consulClientTokenSecret
	d.ConsulServerCASecret = *consulServerCASecret
	d.TLSEnabled = *tlsEnabled
	d.ACLsEnabled = *aclEnabled
	d.ConsulServer = fmt.Sprintf("%s:8301", *consulServer)

	d.ConsulServerHTTPAddr = fmt.Sprintf("http://%s:8500", *consulServer)
	d.ConsulHTTPAddr = "http://localhost:8500"

	if *tlsEnabled {
		d.ConsulHTTPAddr = "https://localhost:8501"
		d.ConsulServerHTTPAddr = fmt.Sprintf("https://%s:8501", *consulServer)
	}

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

	// process the template
	processedTemplate := bytes.NewBuffer([]byte{})
	tmpl, err := template.New("test").Parse(templates.SidecarContainers)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	tmpl.Execute(processedTemplate, d)

	sidecarTemplate := map[string]interface{}{}

	// read the template
	err = yaml.Unmarshal(processedTemplate.Bytes(), &sidecarTemplate)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	data, err := ioutil.ReadFile(*deployment)
	if err != nil {
		fmt.Printf("Unable to read deployment file %s: %s", *deployment, err)
		os.Exit(1)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))

	for {
		var value map[string]interface{}
		err := dec.Decode(&value)
		if err == io.EOF {
			break
		}

		if err != nil {
			fmt.Printf("Unable to process deployment file %s: %s", *deployment, err)
			os.Exit(1)
		}

		fmt.Println("---")

		if value["kind"] != "Deployment" {
			out, _ := yaml.Marshal(value)
			fmt.Fprintln(os.Stdout, string(out))
			continue
		}

		value, err = appendToDeployment(
			value,
			sidecarTemplate["containers"].([]interface{}),
			sidecarTemplate["initContainers"].([]interface{}),
			sidecarTemplate["volumes"].([]interface{}),
		)

		if err != nil {
			fmt.Printf("Unable to add sidecars to deployment %s: %s", *deployment, err)
			os.Exit(1)
		}

		out, err := yaml.Marshal(value)
		if err != nil {
			fmt.Printf("Unable to add sidecars to deployment %s: %s", *deployment, err)
			os.Exit(1)
		}

		fmt.Fprintln(os.Stdout, string(out))

	}

}

func appendToDeployment(deployment map[string]interface{}, containers []interface{}, initContainers []interface{}, volumes []interface{}) (map[string]interface{}, error) {
	// get the containers
	spec, ok := deployment["spec"].(map[interface{}]interface{})
	if !ok {
		fmt.Println(deployment["spec"])
		return nil, fmt.Errorf("Unable to parse deployment. Deployment does not contain a 'spec'")
	}

	template, ok := spec["template"].(map[interface{}]interface{})
	if !ok {
		return nil, fmt.Errorf("Unable to parse deployment. Deployment does not contain a 'template'")
	}

	spec, ok = template["spec"].(map[interface{}]interface{})
	if !ok {
		return nil, fmt.Errorf("Unable to parse deployment. Deployment does not contain a template 'spec'")
	}

	c, ok := spec["containers"].([]interface{})
	if !ok {
		fmt.Printf("%#v", spec["containers"])
		return nil, fmt.Errorf("Unable to parse deployment. Deployment does not contain any 'containers'")
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
