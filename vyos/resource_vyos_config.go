package vyos

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type vyosConfigResource struct {
	client *APIClient
}

func generateConfigID(commands []vyosCommandModel) string {
	hasher := sha256.New()
	for _, cmd := range commands {
		op := cmd.Op.ValueString()
		path := strings.Join(toStringSlice(cmd.Path), "::")
		hasher.Write([]byte(fmt.Sprintf("%s:%s", op, path)))
	}
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

func extractConfigValue(v interface{}) string {
	if v == nil {
		return ""
	}

	switch val := v.(type) {
	case map[string]interface{}:
		if addresses, ok := val["address"].([]interface{}); ok {
			addressStrings := make([]string, len(addresses))
			for i, addr := range addresses {
				addressStrings[i] = fmt.Sprintf("%v", addr)
			}
			sort.Strings(addressStrings)
			result := map[string][]string{"address": addressStrings}
			bytes, _ := json.Marshal(result)
			return string(bytes)
		}

		if len(val) == 1 {
			for _, inner := range val {
				switch nested := inner.(type) {
				case map[string]interface{}:
					if len(nested) == 0 {
						for k := range val {
							return k
						}
					}
				default:
					return fmt.Sprintf("%v", nested)
				}
			}
		}

		bytes, _ := json.Marshal(val)
		return string(bytes)

	case []interface{}:
		strValues := make([]string, len(val))
		for i, v := range val {
			strValues[i] = fmt.Sprintf("%v", v)
		}
		sort.Strings(strValues)
		sortedVal := make([]interface{}, len(strValues))
		for i, s := range strValues {
			sortedVal[i] = s
		}
		bytes, _ := json.Marshal(sortedVal)
		return string(bytes)

	default:
		return fmt.Sprintf("%v", val)
	}
}

func isRoutePath(path []string) bool {
	return len(path) >= 4 &&
		path[0] == "protocols" &&
		path[1] == "static" &&
		path[2] == "route"
}

func getRouteBasePath(path []string) []string {
	routeEndIdx := -1
	for i, part := range path {
		if part == "next-hop" {
			routeEndIdx = i
			break
		}
	}
	if routeEndIdx != -1 {
		return path[:routeEndIdx]
	}
	return path
}

func (r *vyosConfigResource) hasMultipleNextHops(routePath []string) (bool, error) {
	config, err := r.client.GetCurrentConfig(routePath)
	if err != nil {
		return false, err
	}
	if nextHops, ok := config["next-hop"].(map[string]interface{}); ok {
		return len(nextHops) > 1, nil
	}
	return false, nil
}

func NewVyosConfigResource() resource.Resource {
	return &vyosConfigResource{}
}

func (r *vyosConfigResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "vyosconfig_command"
}

func (r *vyosConfigResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
			},
			"commands": schema.ListNestedAttribute{
				Required: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"op": schema.StringAttribute{
							Required:    true,
							Description: "Operation type (set/delete)",
						},
						"path": schema.ListAttribute{
							ElementType: types.StringType,
							Required:    true,
							Description: "Configuration path",
						},
					},
				},
			},
		},
	}
}

func (r *vyosConfigResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.client = req.ProviderData.(*APIClient)
}

func (r *vyosConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vyosConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	commands := processCommandsForAPI(plan.Commands)
	if err := r.client.ApplyCommands(commands); err != nil {
		resp.Diagnostics.AddError("Failed to apply configuration", err.Error())
		return
	}

	newState := vyosConfigModel{
		Commands: plan.Commands,
		ID:       types.StringValue(generateConfigID(plan.Commands)),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func processCommandsForAPI(commands []vyosCommandModel) []Command {
	apiCommands := make([]Command, len(commands))
	for i, cmd := range commands {
		apiCommands[i] = Command{
			Op:   cmd.Op.ValueString(),
			Path: toStringSlice(cmd.Path),
		}
	}
	return apiCommands
}

func (r *vyosConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vyosConfigModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	for _, cmd := range state.Commands {
		if cmd.Op.ValueString() == "set" {
			pathParts := toStringSlice(cmd.Path)
			exists, err := r.client.PathExists(pathParts)
			if err != nil {
				resp.Diagnostics.AddWarning("Error checking existence", err.Error())
				continue
			}

			if !exists {
				if IsRoutePath(pathParts) && len(pathParts) >= 4 {
					routeBasePath := pathParts[:4]
					routeExists, err := r.client.PathExists(routeBasePath)
					if err != nil {
						resp.Diagnostics.AddWarning("Error checking route existence", err.Error())
					} else if !routeExists {
						resp.State.RemoveResource(ctx)
						return
					}
				} else {
					resp.State.RemoveResource(ctx)
					return
				}
			}
		}
	}
}

func (r *vyosConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state vyosConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	keepPaths := make(map[string]bool)
	for _, cmd := range plan.Commands {
		if cmd.Op.ValueString() == "set" {
			pathKey := makePathKey(toStringSlice(cmd.Path))
			keepPaths[pathKey] = true
		}
	}

	deleteCommands := make([]Command, 0)
	for _, cmd := range state.Commands {
		if cmd.Op.ValueString() == "set" {
			pathParts := toStringSlice(cmd.Path)
			pathKey := makePathKey(pathParts)

			if !keepPaths[pathKey] {
				deleteCommands = append(deleteCommands, Command{
					Op:   "delete",
					Path: pathParts,
				})
			}
		}
	}

	if len(deleteCommands) > 0 {
		if err := r.client.ApplyCommands(deleteCommands); err != nil {
			resp.Diagnostics.AddError("Failed to delete old configuration", err.Error())
			return
		}
	}

	newCommands := processCommandsForAPI(plan.Commands)
	if len(newCommands) > 0 {
		if err := r.client.ApplyCommands(newCommands); err != nil {
			resp.Diagnostics.AddError("Failed to apply new configuration", err.Error())
			return
		}
	}

	newState := vyosConfigModel{
		Commands: plan.Commands,
		ID:       types.StringValue(generateConfigID(plan.Commands)),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func makePathKey(path []string) string {
	return strings.Join(path, ":")
}

func (r *vyosConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vyosConfigModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	deleteCommands := make([]Command, 0)

	for _, cmd := range state.Commands {
		if cmd.Op.ValueString() == "set" {
			pathParts := toStringSlice(cmd.Path)

			if isRoutePath(pathParts) && len(pathParts) >= 4 && pathParts[2] == "route" {
				deleteCommands = append(deleteCommands, Command{
					Op:   "delete",
					Path: pathParts[:4],
				})
			} else {
				deleteCommands = append(deleteCommands, Command{
					Op:   "delete",
					Path: pathParts,
				})
			}
		}
	}

	sortedCommands := sortCommandsByPathDepth(deleteCommands)

	if err := r.client.ApplyCommands(sortedCommands); err != nil {
		resp.Diagnostics.AddError("Falha ao excluir configuração", err.Error())
	}
}

func sortCommandsByPathDepth(commands []Command) []Command {
	sort.SliceStable(commands, func(i, j int) bool {
		return len(commands[i].Path) > len(commands[j].Path)
	})
	return commands
}

func toStringSlice(list types.List) []string {
	elements := make([]string, 0, len(list.Elements()))
	for _, elem := range list.Elements() {
		elements = append(elements, elem.(types.String).ValueString())
	}
	return elements
}

func stringSliceToAttrValues(slice []string) []attr.Value {
	attrValues := make([]attr.Value, len(slice))
	for i, v := range slice {
		attrValues[i] = types.StringValue(v)
	}
	return attrValues
}

func getNestedValue(config map[string]interface{}, path []string) interface{} {
	current := config
	for i, part := range path {
		val, exists := current[part]
		if !exists {
			return nil
		}
		if i == len(path)-1 {
			return val
		}
		nested, ok := val.(map[string]interface{})
		if !ok {
			return nil
		}
		current = nested
	}
	return nil
}

type vyosConfigModel struct {
	ID       types.String       `tfsdk:"id"`
	Commands []vyosCommandModel `tfsdk:"commands"`
}

type vyosCommandModel struct {
	Op   types.String `tfsdk:"op"`
	Path types.List   `tfsdk:"path"`
}
