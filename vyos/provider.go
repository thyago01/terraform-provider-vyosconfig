package vyos

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

type VyosProvider struct{}

func New() provider.Provider {
	return &VyosProvider{}
}

func (p *VyosProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "vyosconfig"
	resp.Version = "1.0.2"
}

func (p *VyosProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Required:    true,
				Description: "Hostname or VyOS IP",
			},
			"apikey": schema.StringAttribute{
				Required:    true,
				Description: "ApiKEy",
			},
		},
	}
}

func (p *VyosProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config struct {
		Host   string `tfsdk:"host"`
		Apikey string `tfsdk:"apikey"`
	}

	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	client, err := NewAPIClient(config.Host, config.Apikey)
	if err != nil {
		resp.Diagnostics.AddError("Erro to create API client", err.Error())
		return
	}

	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *VyosProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewVyosConfigResource,
	}
}

func (p *VyosProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return nil
}
