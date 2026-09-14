package neonresource

import (
	"github.com/evatt-labs/kraai/internal/provider/cloudflare"
	"github.com/evatt-labs/kraai/internal/provider/neon"
	"github.com/evatt-labs/kraai/internal/resource"
)

// Register adds the Postgres capability's two resource types.
//
// Registered together because they are one capability: a Postgres binding on
// Cloudflare is a branch and the configuration fronting it (D30), and
// registering the branch without Hyperdrive would produce an environment with
// a database no Worker can reach.
func Register(reg *resource.Registry, neonClient *neon.Client, cfClient *cloudflare.Client, settings BranchSettings) error {
	for _, r := range Registrations(neonClient, cfClient, settings) {
		if err := reg.Register(r); err != nil {
			return err
		}
	}
	return nil
}

// Registrations returns the Postgres capability's registrations, in the phase
// order they are applied.
func Registrations(neonClient *neon.Client, cfClient *cloudflare.Client, settings BranchSettings) []resource.Registration {
	return []resource.Registration{
		{
			Provider: Provider, Type: TypeBranch,
			Capability: Capability,
			// First: everything that binds to a database needs it to exist.
			Phase: resource.PhaseDatabase,
			// Listed and matched on branch name within the project.
			Lookup:   resource.LookupByAttr,
			Resource: &branchResource{client: neonClient, settings: settings},
		},
		{
			Provider: HyperdriveProvider, Type: TypeHyperdrive,
			Capability: Capability,
			// After the branch, whose connection string it consumes.
			Phase:    resource.PhaseStorage,
			Lookup:   resource.LookupByAttr,
			Resource: &hyperdriveResource{client: cfClient},
		},
	}
}

// DecodeSettings reads BranchSettings from a manifest entry's provider
// configuration. Exported so a caller assembling the registry can validate
// settings before wiring anything up, rather than at first use.
func DecodeSettings(config map[string]any) (BranchSettings, error) {
	return decodeSettings(config)
}
