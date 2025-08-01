package routeros

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-routeros/routeros/v3"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

type Client interface {
	GetExtraParams() *ExtraParams
	GetTransport() TransportType
	SendRequest(method crudMethod, url *URL, item MikrotikItem, result interface{}) error
}

type crudMethod int

const (
	crudUnknown crudMethod = iota
	crudCreate
	crudRead
	crudUpdate
	crudDelete
	crudPost
	crudImport
	crudSign
	crudSignViaScep
	crudRemove
	crudRevoke
	crudMove
	crudStart
	crudStop
	crudGenerateKey
)

type ExtraParams struct {
	SuppressSysODelWarn bool
}

func NewClient(ctx context.Context, d *schema.ResourceData) (interface{}, diag.Diagnostics) {

	tlsConf := tls.Config{
		InsecureSkipVerify: d.Get("insecure").(bool),
	}

	caCertificate := d.Get("ca_certificate").(string)
	if tlsConf.InsecureSkipVerify && caCertificate != "" {
		return nil, diag.Errorf("You have selected mutually exclusive options: " +
			"ca_certificate and insecure connection. Please check the ENV variables and TF files.")
	}

	if caCertificate != "" {
		if _, err := os.Stat(caCertificate); err != nil {
			ColorizedDebug(ctx, "Failed to read CA file '"+caCertificate+"', error: "+err.Error())
			return nil, diag.FromErr(err)
		}

		certPool := x509.NewCertPool()
		file, err := os.ReadFile(caCertificate)
		if err != nil {
			ColorizedDebug(ctx, "Failed to read CA file '"+caCertificate+"', error: "+err.Error())
			return nil, diag.Errorf("Failed to read CA file '%s', %v", caCertificate, err)
		}
		certPool.AppendCertsFromPEM(file)
		tlsConf.RootCAs = certPool
	}

	routerUrl, err := url.Parse(d.Get("hosturl").(string))
	if err != nil || routerUrl.Host == "" {
		routerUrl, err = url.Parse("https://" + d.Get("hosturl").(string))
	}
	if err != nil {
		return nil, diag.Diagnostics{
			{
				Severity: diag.Error,
				Summary:  err.Error(),
				Detail:   "Error while parsing the router URL: '" + d.Get("hosturl").(string) + "'",
			},
		}
	}
	routerUrl.Path = strings.TrimSuffix(routerUrl.Path, "/")

	var useTLS = true
	var transport = TransportREST

	// Parse URL.
	switch routerUrl.Scheme {
	case "http":
	case "https":
	case "apis":
		routerUrl.Scheme = ""
		if routerUrl.Port() == "" {
			routerUrl.Host += ":8729"
		}
		transport = TransportAPI
	case "api":
		routerUrl.Scheme = ""
		if routerUrl.Port() == "" {
			routerUrl.Host += ":8728"
		}
		useTLS = false
		transport = TransportAPI
	default:
		panic("[NewClient] wrong transport type: " + routerUrl.Scheme)
	}

	RouterOSVersion = d.Get("routeros_version").(string)
	if RouterOSVersion != "" {
		ColorizedMessage(ctx, INFO, "RouterOS from env: "+RouterOSVersion)
	}

	if transport == TransportAPI {
		api := &ApiClient{
			ctx:       ctx,
			HostURL:   routerUrl.Host,
			Username:  d.Get("username").(string),
			Password:  d.Get("password").(string),
			Transport: TransportAPI,
			extra: &ExtraParams{
				SuppressSysODelWarn: d.Get("suppress_syso_del_warn").(bool),
			},
		}

		if useTLS {
			api.Client, err = routeros.DialTLS(api.HostURL, api.Username, api.Password, &tlsConf)
		} else {
			api.Client, err = routeros.Dial(api.HostURL, api.Username, api.Password)
		}
		if err != nil {
			return nil, diag.FromErr(err)
		}

		// The synchronous client has an infinite wait issue
		// when an error occurs while creating multiple resources.
		api.Async()

		if RouterOSVersion == "" {
			ros, diags := GetRouterOSVersion(api)
			if diags != nil {
				return nil, diags
			}

			RouterOSVersion = ros
			ColorizedMessage(ctx, INFO, "RouterOS: "+RouterOSVersion)
		}

		return api, nil
	}

	rest := &RestClient{
		ctx:       ctx,
		HostURL:   routerUrl.String(),
		Username:  d.Get("username").(string),
		Password:  d.Get("password").(string),
		Transport: TransportREST,
		extra: &ExtraParams{
			SuppressSysODelWarn: d.Get("suppress_syso_del_warn").(bool),
		},
	}

	rest.Client = &http.Client{
		// ... By default, CreateContext has a 20 minute timeout ...
		// but MT REST API timeout is in 60 seconds for any operation.
		// Make the timeout smaller so that the lifetime of the context is less than the lifetime of the session.
		Timeout: time.Duration(d.Get("rest_timeout").(int)) * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tlsConf,
		},
	}

	if RouterOSVersion == "" {
		ros, diags := GetRouterOSVersion(rest)
		if diags != nil {
			return nil, diags
		}

		RouterOSVersion = ros
		ColorizedMessage(ctx, INFO, "RouterOS: "+RouterOSVersion)
	}

	return rest, nil
}

type URL struct {
	Path  string   // URL path without '/rest'.
	Query []string // Query values.
}

// GetApiCmd Returns the set of commands for the API client.
func (u *URL) GetApiCmd() []string {
	res := []string{u.Path}
	//if len(u.Query) > 0 && u.Query[len(u.Query) - 1] != "?#|" {
	//	u.Query = append(u.Query, "?#|")
	//}
	return append(res, u.Query...)
}

// GetRestURL Returns the URL for the client
func (u *URL) GetRestURL() string {
	q := strings.Join(u.Query, "&")
	if len(q) > 0 && q[0] != '?' {
		q = "?" + q
	}
	return u.Path + q
}

// EscapeChars peterGo https://groups.google.com/g/golang-nuts/c/NiQiAahnl5E/m/U60Sm1of-_YJ
func EscapeChars(data []byte) []byte {
	var u = []byte(`\u0000`)
	//var u = []byte(`U+0000`)
	var res = make([]byte, 0, len(data))

	for i, ch := range data {
		if ch < 0x20 {
			res = append(res, u...)
			hex.Encode(res[len(res)-2:], data[i:i+1])
			continue
		}
		res = append(res, ch)
	}
	return res
}

// Obtain a version of RouterOS to automatically customize resource schemas.
func GetRouterOSVersion(m interface{}) (string, diag.Diagnostics) {
	res, err := ReadItems(nil, "/system/resource", m.(Client))
	if err != nil {
		return "", diag.FromErr(err)
	}

	// Resource not found.
	if len(*res) == 0 {
		return "", diag.Errorf("RouterOS version not found")
	}

	version, ok := (*res)[0]["version"]
	if !ok {
		return "", diag.Errorf("RouterOS version not found")
	}

	// d.d | d.d.d
	re := regexp.MustCompile(`^(\d+\.){1,2}\d+`)

	if !re.MatchString(version) {
		return "", diag.Errorf("RouterOS version not found")
	}

	return re.FindString(version), nil
}
