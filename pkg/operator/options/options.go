/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package options

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
)

type Options struct {
	ShapeMetaFile                          string
	ShapeMetaRefreshIntervalHours          int
	ClusterCompartmentId                   string
	VcnCompartmentId                       string
	OciVcnIpNative                         bool
	PreBakedImageCompartmentId             string
	ApiserverEndpoint                      string
	IpFamiliesFlag                         *network.IpFamilyValue
	OciAuthMethods                         AuthMethod
	OciProfileName                         string
	GlobalShapeConfigs                     []ociv1beta1.ShapeConfig
	RepairPolicies                         []cloudprovider.RepairPolicy
	InstanceLaunchTimeoutVMMins            int
	InstanceLaunchTimeoutBMMins            int
	InstanceOperationPollIntervalInSeconds int
	InstanceLaunchTimeOutFailOver          bool
	DisableRateLimiter                     bool
	RateLimitQPSRead                       float64
	RateLimitBurstRead                     int
	RateLimitQPSWrite                      float64
	RateLimitBurstWrite                    int
	setFlags                               map[string]bool
	parsed                                 bool
}

type optionsKey struct{}

type AuthMethod string

const (
	AuthByWORKLOAD          AuthMethod = "WORKLOAD"
	AuthBySession           AuthMethod = "SESSION"
	AuthByInstancePrincipal AuthMethod = "INSTANCE_PRINCIPAL"
)

func init() {
	coreoptions.Injectables = append(coreoptions.Injectables, &Options{
		IpFamiliesFlag: new(network.IpFamilyValue),
	})
}

func (o *Options) AddFlags(fs *coreoptions.FlagSet) {
	if o.parsed {
		return
	}

	// mandatory configures
	fs.StringVar(&o.ClusterCompartmentId, "cluster-compartment-id",
		"", "the compartment ocid which the cluster runs in")
	fs.StringVar(&o.VcnCompartmentId, "vcn-compartment-id", "",
		"the compartment ocid which the cluster virtual network resides")
	fs.StringVar(&o.ApiserverEndpoint, "apiserver-endpoint",
		"", "the apiserver endpoint for nodes to register")

	// optional but needed in special cases: cluster features
	fs.BoolVar(&o.OciVcnIpNative, "oci-vcn-ip-native",
		false, "whether the cluster runs with oci vcn ip native cni")
	fs.Var(o.IpFamiliesFlag, "ip-families",
		"comma separated ip families, accept values: \"IPv4\", \"IPv6\"")

	// optional but needed in special deployment cases
	fs.StringVar((*string)(&o.OciAuthMethods), "oci-auth-method",
		string(AuthByWORKLOAD),
		"The auth method to access oracle cloud resource, support WORKLOAD, SESSION, INSTANCE_PRINCIPAL")
	fs.StringVar(&o.OciProfileName, "oci-profile-name", "DEFAULT",
		"OCI profile name to use for running karpenter controller locally")

	// optional for advanced control
	fs.Var(
		(*GlobalFlexShapeConfigsValue)(&o.GlobalShapeConfigs),
		"flexible-shape-configs",
		`A JSON array of shape config entries, usable as global flexible shape defaults across node classes.
Example in a JSON format:
  --flexible-shape-configs='[
    {"ocpus": 2, "memoryInGbs": 16},
    {"ocpus": 4, "memoryInGbs": 32, "baselineOcpuUtilization":"BASELINE_1_2"}
  ]'`,
	)
	fs.IntVar(&o.ShapeMetaRefreshIntervalHours, "shape-meta-refresh-interval-hours", 24,
		"Interval between shape meta configmap is read by KPO, in hours (integer). Example: 24")

	// TODO: these two settings are tricky to adjust so should be revisited, e.g make InstanceLaunchTimeoutVMMins too
	// small may cause instance launch prematurely cancelled due to a slow hypervisor placement.
	fs.IntVar(&o.InstanceLaunchTimeoutVMMins, "instance-launch-timeout-vm-mins", 5,
		"Timeout in minutes to wait for OCI work request for VM instance launch")
	fs.IntVar(&o.InstanceLaunchTimeoutBMMins, "instance-launch-timeout-bm-mins", 60,
		"Timeout in minutes to wait for OCI work request for BareMetal instance launch")

	fs.IntVar(&o.InstanceOperationPollIntervalInSeconds, "instance-operation-poll-interval-seconds", 5,
		"Intervals in seconds to decide how often instance operation result is checked")
	fs.BoolVar(&o.InstanceLaunchTimeOutFailOver, "instance-launch-timeout-failover", false,
		"When instance launch timeout happens, fail over to next instance types")
	fs.BoolVar(&o.DisableRateLimiter, "disable-rate-limiter", true,
		"Disable the OCI client-side rate limiter")
	fs.Float64Var(&o.RateLimitQPSRead, "rate-limit-qps-read", 0,
		"Read QPS for the OCI client-side rate limiter. 0 uses the default")
	fs.IntVar(&o.RateLimitBurstRead, "rate-limit-burst-read", 0,
		"Read burst for the OCI client-side rate limiter. 0 uses the default")
	fs.Float64Var(&o.RateLimitQPSWrite, "rate-limit-qps-write", 0,
		"Write QPS for the OCI client-side rate limiter. 0 uses the default")
	fs.IntVar(&o.RateLimitBurstWrite, "rate-limit-burst-write", 0,
		"Write burst for the OCI client-side rate limiter. 0 uses the default")

	fs.Var((*RepairPoliciesValue)(&o.RepairPolicies),
		"repair-policies",
		"A JSON array of karpenter provider repair policies. Example in a JSON format"+
			"[{\"ConditionType\": \"Ready\",\"ConditionStatus\": \"False\",\"TolerationDuration\": \"600000000000\"}]")

	fs.StringVar(&o.PreBakedImageCompartmentId, "pre-baked-image-compartment-id",
		"", "the compartment ocid under which OKE pre-baked images are published")
	fs.StringVar(&o.ShapeMetaFile, "shape-meta-file",
		"", "shape meta file for OCI")
}

func (o *Options) Parse(fs *coreoptions.FlagSet, args ...string) error {
	if o.parsed {
		return nil
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		return fmt.Errorf("parsing flags, %w", err)
	}

	o.setFlags = map[string]bool{}
	cliFlags := sets.New[string]()
	fs.Visit(func(f *flag.Flag) {
		cliFlags.Insert(f.Name)
	})

	fs.VisitAll(func(f *flag.Flag) {
		if _, ok := cliFlags[f.Name]; !ok {
			envName := strings.ReplaceAll(strings.ToUpper(f.Name), "-", "_")
			env, envOk := os.LookupEnv(envName)

			if envOk {
				log.Printf("env variable: %s, %s", envName, env)

				err := f.Value.Set(env)
				if err != nil {
					panic(err)
				}
			}
		}
	})

	if err := o.Validate(); err != nil {
		return fmt.Errorf("validating options, %w", err)
	}

	o.parsed = true
	return nil
}

func (o *Options) Validate() error {
	// Optionally: validate global shapeConfigs (all required fields present)
	for i, cfg := range o.GlobalShapeConfigs {
		if cfg.Ocpus == nil || *cfg.Ocpus < 1.0 {
			return fmt.Errorf("global-shape-configs[%d] ocpus must be >= 1", i)
		}
	}

	if o.ClusterCompartmentId == "" {
		return errors.New("cluster-compartment-id is missing")
	}

	if o.VcnCompartmentId == "" {
		return errors.New("vcn-compartment-id is missing")
	}

	if o.PreBakedImageCompartmentId == "" {
		return errors.New("pre-baked-image-compartment-id is missing")
	}

	if o.ApiserverEndpoint == "" {
		return errors.New("apiserver-endpoint is missing")
	}

	if o.ShapeMetaRefreshIntervalHours <= 0 {
		return errors.New("shape-meta-refresh-interval-hours must be a positive integer")
	}

	if o.InstanceLaunchTimeoutVMMins <= 0 {
		return errors.New("instance-launch-timeout-vm-mins must be a positive integer")
	}

	if o.InstanceLaunchTimeoutBMMins <= 0 {
		return errors.New("delete-instance-timeout-bm-mins must be a positive integer")
	}
	if o.RateLimitQPSRead < 0 {
		return errors.New("rate-limit-qps-read must be greater than or equal to 0")
	}
	if o.RateLimitBurstRead < 0 {
		return errors.New("rate-limit-burst-read must be greater than or equal to 0")
	}
	if o.RateLimitQPSWrite < 0 {
		return errors.New("rate-limit-qps-write must be greater than or equal to 0")
	}
	if o.RateLimitBurstWrite < 0 {
		return errors.New("rate-limit-burst-write must be greater than or equal to 0")
	}

	return nil
}

func (o *Options) ToContext(ctx context.Context) context.Context {
	return ToContext(ctx, o)
}

func ToContext(ctx context.Context, opts *Options) context.Context {
	return context.WithValue(ctx, optionsKey{}, opts)
}

// GlobalFlexShapeConfigsValue is a flag.Value/json flag decoding helper for []ShapeConfig.
type GlobalFlexShapeConfigsValue []ociv1beta1.ShapeConfig

func (a *GlobalFlexShapeConfigsValue) Set(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		*a = nil
		return nil
	}

	return json.Unmarshal([]byte(s), a)
}
func (a *GlobalFlexShapeConfigsValue) String() string {
	b, err := json.Marshal(a)
	if err != nil {
		// Return an error string including the marshal error
		return fmt.Sprintf("error: %v", err)
	}
	return string(b)
}

type RepairPoliciesValue []cloudprovider.RepairPolicy

func (r *RepairPoliciesValue) Set(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		*r = nil
		return nil
	}

	return json.Unmarshal([]byte(s), r)
}

func (r *RepairPoliciesValue) String() string {
	b, err := json.Marshal(r)
	if err != nil {
		// Return an error string including the marshal error
		return fmt.Sprintf("error: %v", err)
	}
	return string(b)
}

func FromContext(ctx context.Context) *Options {
	retval := ctx.Value(optionsKey{})
	if retval == nil {
		panic("options doesn't exist in context")
	}
	return retval.(*Options)
}
