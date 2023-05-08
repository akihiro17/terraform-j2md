package terraform

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/template"

	"github.com/hashicorp/terraform-json/sanitize"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/pmezard/go-difflib/difflib"
)

const planTemplateBody = `### {{len .CreatedAddresses}} to add, {{len .UpdatedAddresses}} to change, {{len .DeletedAddresses}} to destroy, {{len .ReplacedAddresses}} to replace.
{{- if .CreatedAddresses}}
- add{{ range .CreatedAddresses }}
    - {{. -}}
{{end}}{{end}}
{{- if .UpdatedAddresses}}
- change{{ range .UpdatedAddresses }}
    - {{. -}}
{{end}}{{end}}
{{- if .DeletedAddresses}}
- destroy{{ range .DeletedAddresses }}
    - {{. -}}
{{end}}{{end}}
{{- if .ReplacedAddresses}}
- replace{{ range .ReplacedAddresses }}
    - {{. -}}
{{end}}{{end}}
{{if .ResourceChanges -}}
<details><summary>Change details</summary>
{{ range .ResourceChanges }}
{{codeFence}}diff
# {{.ResourceChange.Type}}.{{.ResourceChange.Name}} {{.HeaderSuffix}}
{{.GetUnifiedDiffString}}{{codeFence}}
{{end}}
</details>
{{end}}`

type PlanData struct {
	CreatedAddresses  []string
	UpdatedAddresses  []string
	DeletedAddresses  []string
	ReplacedAddresses []string
	ResourceChanges   []ResourceChangeData
}
type ResourceChangeData struct {
	ResourceChange *tfjson.ResourceChange
}

func (p *PlanData) formatJsonString() error {
	for _, r := range p.ResourceChanges {
		if err := r.formatJsonString(); err != nil {
			return err
		}
	}
	return nil
}

func (r *ResourceChangeData) formatJsonString() error {
	if _, err := r.format(r.ResourceChange.Change.Before); err != nil {
		return err
	}
	if _, err := r.format(r.ResourceChange.Change.After); err != nil {
		return err
	}
	return nil
}

func (r *ResourceChangeData) format(v any) (any, error) {
	switch x := v.(type) {
	case []any:
		for i, v := range x {
			result, err := r.format(v)
			if err != nil {
				return nil, err
			}
			x[i] = result
		}
	case map[string]any:
		for k, v := range x {
			result, err := r.format(v)
			if err != nil {
				return nil, err
			}
			// fmt.Printf("key: %v value: %v\n", k, v)
			x[k] = result
		}
	case string:
		var j json.RawMessage
		if !json.Valid([]byte(v.(string))) {
			return v, nil
		}
		if err := json.Unmarshal([]byte(v.(string)), &j); err == nil {
			a, err := json.MarshalIndent(j, "", "  ")
			if err != nil {
				return nil, err
			}
			v = string(a)
			return v, nil
		}
	default:
		return v, nil
	}

	return v, nil
}

func (r ResourceChangeData) GetUnifiedDiffString() (string, error) {
	before, err := json.MarshalIndent(r.ResourceChange.Change.Before, "", "  ")
	if err != nil {
		return "", fmt.Errorf("invalid resource changes (before): %w", err)
	}
	after, err := json.MarshalIndent(r.ResourceChange.Change.After, "", "  ")
	if err != nil {
		return "", fmt.Errorf("invalid resource changes (after) : %w", err)
	}
	// Try to parse JSON string in values
	replacer := strings.NewReplacer(`\n`, "\n  ", `\"`, "\"")
	diff := difflib.UnifiedDiff{
		A:       difflib.SplitLines(replacer.Replace(string(before))),
		B:       difflib.SplitLines(replacer.Replace(string(after))),
		Context: 3,
	}
	diffText, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		return "", fmt.Errorf("failed to create diff: %w", err)
	}

	return diffText, nil
}

func (r ResourceChangeData) HeaderSuffix() string {
	switch {
	case r.ResourceChange.Change.Actions.Create():
		return "will be created"
	case r.ResourceChange.Change.Actions.Update():
		return "will be updated in-place"
	case r.ResourceChange.Change.Actions.Delete():
		return "will be destroyed"
	case r.ResourceChange.Change.Actions.Replace():
		return "will be replaced"
	}
	return ""
}

func (plan *PlanData) Render(w io.Writer) error {
	funcMap := template.FuncMap{
		"codeFence": func() string {
			return "````````"
		},
	}
	planTemplate, err := template.New("plan").Funcs(funcMap).Parse(planTemplateBody)
	if err != nil {
		return fmt.Errorf("invalid template text: %w", err)
	}

	if err := planTemplate.Execute(w, plan); err != nil {
		return fmt.Errorf("failed to render template: %w", err)
	}
	return nil
}

func NewPlanData(input []byte) (*PlanData, error) {
	var plan tfjson.Plan
	if err := json.Unmarshal(input, &plan); err != nil {
		return nil, fmt.Errorf("cannot parse input: %w", err)
	}
	sanitizedPlan, err := sanitize.SanitizePlan(&plan)
	if err != nil {
		return nil, fmt.Errorf("failed to sanitize plan: %w", err)
	}
	planData := PlanData{}
	for _, c := range sanitizedPlan.ResourceChanges {
		if c.Change.Actions.NoOp() || c.Change.Actions.Read() {
			continue
		}

		switch {
		case c.Change.Actions.Create():
			planData.CreatedAddresses = append(planData.CreatedAddresses, c.Address)
		case c.Change.Actions.Update():
			planData.UpdatedAddresses = append(planData.UpdatedAddresses, c.Address)
		case c.Change.Actions.Delete():
			planData.DeletedAddresses = append(planData.DeletedAddresses, c.Address)
		case c.Change.Actions.Replace():
			planData.ReplacedAddresses = append(planData.ReplacedAddresses, c.Address)
		}
		planData.ResourceChanges = append(planData.ResourceChanges, ResourceChangeData{
			ResourceChange: c,
		})
	}
	if err := planData.formatJsonString(); err != nil {
		return nil, err
	}
	return &planData, nil
}
