package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/orpheus-agents/orpheus/agentcontract"
	"github.com/orpheus-agents/orpheus/internal/servicecredentials"
	"github.com/orpheus-agents/orpheus/internal/session"
)

var serviceCode = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type Service struct {
	AssignmentProvider *servicecredentials.Config `toml:"assignment_provider"`
	Provider           *servicecredentials.Config `toml:"provider"`
	Name               string                     `toml:"name"`
	Description        string                     `toml:"description"`
	EnvFrom            []string                   `toml:"env_from"`
	BeforeRun          *string                    `toml:"before_run"`
}

// ProviderFor freezes the chosen issuer into this run. A failed signed proof is never retried as unsigned.
func (s Service) ProviderFor(access *session.AccessContext) *servicecredentials.Config {
	if s.AssignmentProvider != nil && access != nil && access.Assignment != nil {
		return s.AssignmentProvider
	}
	return s.Provider
}

func validateServices(services map[string]Service) error {
	projected := make(map[string]bool)
	for _, service := range services {
		for _, provider := range []*servicecredentials.Config{service.Provider, service.AssignmentProvider} {
			if provider != nil {
				for _, code := range provider.ProjectServices {
					projected[code] = true
				}
			}
		}
	}
	for code, service := range services {
		service.Name = strings.TrimSpace(service.Name)
		service.Description = strings.TrimSpace(service.Description)
		if !serviceCode.MatchString(code) || service.Name == "" || service.Description == "" || (len(service.EnvFrom) == 0 && service.Provider == nil && service.BeforeRun == nil && !projected[code]) {
			return fmt.Errorf("invalid service %q: code, name, description and environment references, provider or before_run are required", code)
		}
		if service.BeforeRun != nil {
			if err := validateServiceBeforeRun(*service.BeforeRun); err != nil {
				return fmt.Errorf("invalid service %q before_run: %w", code, err)
			}
		}
		if err := validateEnvironment(nil, service.EnvFrom, nil); err != nil {
			return fmt.Errorf("invalid service %q environment references", code)
		}
		if signed := service.AssignmentProvider; signed != nil {
			if service.Provider == nil || service.Provider.Protocol != "" || signed.Protocol != agentcontract.Version ||
				signed.SecretEnv == service.Provider.SecretEnv || signed.TokenFile != service.Provider.TokenFile {
				return fmt.Errorf("service %q requires distinct unsigned and signed issuers with the same token file", code)
			}
		}
		for _, provider := range []*servicecredentials.Config{service.Provider, service.AssignmentProvider} {
			if provider == nil {
				continue
			}
			if err := provider.Validate(); err != nil {
				return fmt.Errorf("invalid service %q provider: %w", code, err)
			}
			for _, projected := range provider.ProjectServices {
				if _, ok := services[projected]; !ok {
					return fmt.Errorf("unknown projected service %q", projected)
				}
			}
		}
		slices.Sort(service.EnvFrom)
		services[code] = service
	}
	return nil
}

// EnvironmentAllowlist authorizes catalog references without duplicating them
// in deployment ENV. Selection still determines which references are forwarded.
func (p Profiles) EnvironmentAllowlist(explicit []string) []string {
	names := slices.Clone(explicit)
	for _, service := range p.Services {
		names = append(names, service.EnvFrom...)
	}
	for _, service := range p.Services {
		for _, provider := range []*servicecredentials.Config{service.Provider, service.AssignmentProvider} {
			if provider == nil {
				continue
			}
			for _, connector := range provider.AudienceConnectors {
				names = slices.DeleteFunc(names, func(name string) bool { return name == connector.SecretEnv })
			}
			names = slices.DeleteFunc(names, func(name string) bool {
				return name == provider.SecretEnv || name == provider.AudienceSecretEnv
			})
		}
	}
	names = slices.DeleteFunc(names, func(name string) bool { return name == p.InputMasker.TokenEnv })
	slices.Sort(names)
	return slices.Compact(names)
}

// ResolveServices freezes public service metadata and merges references. It must
// run only after idempotent replay, never when restoring accepted execution.
func ResolveServices(env map[string]string, from, codes []string, p Profiles, prefix ...any) ([]string, []session.Service, error) {
	if err := validateEnvironment(env, from, prefix); err != nil {
		return nil, nil, err
	}
	seen := make(map[string]bool, len(codes))
	services := make([]session.Service, 0, len(codes))
	refs := append([]string{}, from...)
	for i, code := range codes {
		path := append(slices.Clone(prefix), "services", i)
		if !serviceCode.MatchString(code) || seen[code] {
			return nil, nil, invalid("Service codes must be valid and unique.", path...)
		}
		seen[code] = true
		service, ok := p.Services[code]
		if !ok {
			problem := invalid(fmt.Sprintf("Unknown service %q.", code), path...)
			problem.Problem.Code = "unknown_service"
			return nil, nil, problem
		}
		names := slices.Sorted(slices.Values(service.EnvFrom))
		if names == nil {
			names = []string{}
		}
		for _, name := range names {
			if _, exists := env[name]; exists {
				return nil, nil, invalid(fmt.Sprintf("Service %q conflicts with explicit environment variable %q.", code, name), path...)
			}
		}
		services = append(services, session.Service{Code: code, Name: service.Name, Description: service.Description, EnvFrom: names})
		refs = append(refs, names...)
	}
	slices.SortFunc(services, func(a, b session.Service) int { return strings.Compare(a.Code, b.Code) })
	slices.Sort(refs)
	refs = slices.Compact(refs)
	if err := validateEnvironment(env, refs, prefix); err != nil {
		return nil, nil, err
	}
	return refs, services, nil
}
