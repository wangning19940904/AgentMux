package tools

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/wangning19940904/AgentMux/framework"
)

const (
	BundleComponentCLI       = "cli"
	BundleComponentFramework = "framework"
)

type BundleComponentSpec struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type BundleSpec struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Note             string                `json:"note,omitempty"`
	InternalOnly     bool                  `json:"internal_only,omitempty"`
	InstallPlatforms []string              `json:"install_platforms,omitempty"`
	Components       []BundleComponentSpec `json:"components"`
}

type BundleComponentStatus struct {
	Spec      BundleComponentSpec `json:"spec"`
	Installed bool                `json:"installed"`
	Ready     bool                `json:"ready"`
	Version   string              `json:"version,omitempty"`
	Detail    string              `json:"detail,omitempty"`
}

type BundleStatus struct {
	Spec            BundleSpec              `json:"spec"`
	Installed       bool                    `json:"installed"`
	ReadyComponents int                     `json:"ready_components"`
	TotalComponents int                     `json:"total_components"`
	Components      []BundleComponentStatus `json:"components"`
	Detail          string                  `json:"detail,omitempty"`
}

type BundleInstallOptions struct {
	AcknowledgeInternal bool
}

type BundleComponentResult struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Version string `json:"version,omitempty"`
	Command string `json:"command,omitempty"`
	Log     string `json:"log,omitempty"`
	Error   string `json:"error,omitempty"`
}

type BundleInstallResult struct {
	ID         string                  `json:"id"`
	OK         bool                    `json:"ok"`
	Components []BundleComponentResult `json:"components,omitempty"`
	Error      string                  `json:"error,omitempty"`
}

var bundleCatalog = []BundleSpec{
	{
		ID: "bytedance-internal", Name: "ByteDance Internal Toolkit",
		Note:         "Installs ByteDance developer CLIs, the CIS companion Skill, and the TRAE agent runtime.",
		InternalOnly: true, InstallPlatforms: []string{"darwin", "linux"},
		Components: []BundleComponentSpec{
			{Kind: BundleComponentCLI, ID: "bytedcli", Name: "bytedcli"},
			{Kind: BundleComponentCLI, ID: "cis-cli", Name: "CIS CLI + Skill"},
			{Kind: BundleComponentFramework, ID: "traecli", Name: "TRAE CLI"},
		},
	},
}

func DetectBundles(ctx context.Context) []BundleStatus {
	out := make([]BundleStatus, 0, len(bundleCatalog))
	for _, spec := range bundleCatalog {
		out = append(out, DetectBundle(ctx, spec))
	}
	return out
}

func DetectBundle(ctx context.Context, spec BundleSpec) BundleStatus {
	status := BundleStatus{Spec: spec, TotalComponents: len(spec.Components)}
	if !bundlePlatformSupported(spec, runtime.GOOS) {
		status.Detail = bundlePlatformError(spec, runtime.GOOS)
	}
	for _, component := range spec.Components {
		item := detectBundleComponent(ctx, component)
		status.Components = append(status.Components, item)
		if item.Ready {
			status.ReadyComponents++
		}
	}
	status.Installed = status.TotalComponents > 0 && status.ReadyComponents == status.TotalComponents
	return status
}

func detectBundleComponent(ctx context.Context, component BundleComponentSpec) BundleComponentStatus {
	status := BundleComponentStatus{Spec: component}
	switch component.Kind {
	case BundleComponentCLI:
		spec, ok := LookupCLI(component.ID)
		if !ok {
			status.Detail = "unknown CLI"
			return status
		}
		cli := DetectCLI(ctx, spec)
		status.Installed, status.Ready, status.Version, status.Detail = cli.Installed, cli.Installed, cli.Version, cli.Detail
		for _, skill := range cli.LinkedSkills {
			if !skill.Installed || !skill.InSync {
				status.Ready = false
				status.Detail = skill.Detail
			}
		}
	case BundleComponentFramework:
		spec, ok := framework.Lookup(component.ID)
		if !ok {
			status.Detail = "unknown framework"
			return status
		}
		item := framework.Detect(spec, framework.DetectPrereqs())
		status.Installed, status.Ready, status.Version, status.Detail = item.Installed, item.Installed, item.Version, item.Detail
	default:
		status.Detail = "unknown component kind"
	}
	return status
}

func InstallBundle(ctx context.Context, id string, options BundleInstallOptions) BundleInstallResult {
	return InstallBundleWithProgress(ctx, id, options, nil)
}

func InstallBundleWithProgress(ctx context.Context, id string, options BundleInstallOptions, progress ProgressFunc) BundleInstallResult {
	id = strings.TrimSpace(id)
	result := BundleInstallResult{ID: id}
	spec, ok := LookupBundle(id)
	if !ok {
		result.Error = fmt.Sprintf("unknown bundle %q", id)
		return result
	}
	if spec.InternalOnly && !options.AcknowledgeInternal {
		result.Error = fmt.Sprintf("bundle %q is only available inside ByteDance; explicit acknowledgement is required", id)
		return result
	}
	if !bundlePlatformSupported(spec, runtime.GOOS) {
		result.Error = bundlePlatformError(spec, runtime.GOOS)
		return result
	}

	reportProgress(progress, "preparing", spec.Name, 3)
	failed := false
	for index, component := range spec.Components {
		start := 5 + index*90/max(1, len(spec.Components))
		end := 5 + (index+1)*90/max(1, len(spec.Components))
		item := installBundleComponent(ctx, component, start, end, progress)
		result.Components = append(result.Components, item)
		if !item.OK {
			failed = true
		}
	}
	result.OK = !failed
	if failed {
		result.Error = "one or more bundle components failed; successful components were kept"
	}
	return result
}

func installBundleComponent(ctx context.Context, component BundleComponentSpec, start, end int, progress ProgressFunc) BundleComponentResult {
	result := BundleComponentResult{Kind: component.Kind, ID: component.ID}
	componentProgress := func(phase, detail string, percent int) {
		mapped := start + (end-start)*percent/100
		if detail == "" {
			detail = component.Name
		} else {
			detail = component.Name + ": " + detail
		}
		reportProgress(progress, phase, detail, mapped)
	}
	status := detectBundleComponent(ctx, component)
	if status.Ready {
		result.OK, result.Skipped, result.Version = true, true, status.Version
		componentProgress("verifying", component.Name+" is already ready", 100)
		return result
	}

	switch component.Kind {
	case BundleComponentCLI:
		if status.Installed && component.ID == "cis-cli" {
			res := SyncCLILinkedSkillsWithProgress(ctx, component.ID, componentProgress)
			result.OK, result.Version, result.Command, result.Log, result.Error = res.OK, res.Version, res.Command, res.Log, res.Error
			return result
		}
		res := InstallCLIWithProgressOptions(ctx, component.ID, "install", CLIInstallOptions{AcknowledgeInternal: true}, componentProgress)
		result.OK, result.Version, result.Command, result.Log, result.Error = res.OK, res.Version, res.Command, res.Log, res.Error
	case BundleComponentFramework:
		res := framework.InstallWithProgressOptions(ctx, component.ID, framework.InstallOptions{AcknowledgeInternal: true}, framework.ProgressFunc(componentProgress))
		result.OK, result.Version, result.Command, result.Log, result.Error = res.OK, res.Version, res.Command, res.Log, res.Error
	default:
		result.Error = "unknown component kind"
	}
	return result
}

func LookupBundle(id string) (BundleSpec, bool) {
	for _, spec := range bundleCatalog {
		if spec.ID == strings.TrimSpace(id) {
			return spec, true
		}
	}
	return BundleSpec{}, false
}

func bundlePlatformSupported(spec BundleSpec, goos string) bool {
	if len(spec.InstallPlatforms) == 0 {
		return true
	}
	for _, candidate := range spec.InstallPlatforms {
		if candidate == goos {
			return true
		}
	}
	return false
}

func bundlePlatformError(spec BundleSpec, goos string) string {
	if spec.ID == "bytedance-internal" && goos == "windows" {
		return "ByteDance Internal Toolkit automatic installation is unsupported on native Windows; run AgentMux inside WSL"
	}
	return fmt.Sprintf("bundle %q cannot be installed automatically on %s", spec.ID, goos)
}
