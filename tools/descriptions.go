package tools

import "context"

// DescriptionStore persists user descriptions independently of CLI installs.
type DescriptionStore interface {
	GetSetting(context.Context, string) (string, bool, error)
	SetSetting(context.Context, string, string) error
}

// CLIDescription is shared by the catalog view and agent prompt resolver.
// An explicitly empty description is distinct from an absent override.
func CLIDescription(ctx context.Context, st DescriptionStore, spec CLISpec) (string, error) {
	if st != nil {
		description, ok, err := st.GetSetting(ctx, "tools.cli.description."+spec.ID)
		if err != nil {
			return "", err
		}
		if ok {
			return description, nil
		}
	}
	return spec.Note, nil
}

func SetCLIDescription(ctx context.Context, st DescriptionStore, id, description string) error {
	return st.SetSetting(ctx, "tools.cli.description."+id, description)
}
