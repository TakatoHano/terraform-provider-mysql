package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/TakatoHano/terraform-provider-mysql/mysql/port_forward"
	"github.com/go-sql-driver/mysql"
	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/go-version"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"

	"golang.org/x/net/proxy"
)

const (
	cleartextPasswords = "cleartext"
	nativePasswords    = "native"
)

type MySQLConfiguration struct {
	Config          *mysql.Config
	MaxConnLifetime time.Duration
	MaxOpenConns    int

	mu     sync.Mutex
	db     *sql.DB
	tunnel *port_forward.Tunnel
	// tunnelKey identifies the tunnel's settings so a later providerConfigure
	// call with the same settings can reuse it. Empty when there is no tunnel.
	tunnelKey string
}

var (
	activeConfigsMu sync.Mutex
	activeConfigs   []*MySQLConfiguration
)

// Close closes the cached database handle (if any) and the port-forward
// tunnel (if any). It is safe to call more than once.
func (c *MySQLConfiguration) Close() error {
	var errs error
	c.mu.Lock()
	db := c.db
	c.db = nil
	c.mu.Unlock()
	if db != nil {
		if err := db.Close(); err != nil {
			errs = multierror.Append(errs, err)
		}
	}
	if err := c.tunnel.Close(); err != nil {
		errs = multierror.Append(errs, err)
	}
	return errs
}

// Cleanup closes every configuration created by providerConfigure. It is
// meant to run after plugin.Serve returns.
func Cleanup() error {
	activeConfigsMu.Lock()
	configs := activeConfigs
	activeConfigs = nil
	activeConfigsMu.Unlock()

	var errs error
	for _, c := range configs {
		if err := c.Close(); err != nil {
			errs = multierror.Append(errs, err)
		}
	}
	return errs
}

func Provider() *schema.Provider {
	return &schema.Provider{
		Schema: map[string]*schema.Schema{
			"endpoint": {
				Type:        schema.TypeString,
				Description: "The address of the MySQL server to use. Most often a \"hostname:port\" pair, but may also be an absolute path to a Unix socket when the host OS is Unix-compatible. Can also be sourced from the `MYSQL_ENDPOINT` environment variable.",
				Required:    true,
				DefaultFunc: schema.EnvDefaultFunc("MYSQL_ENDPOINT", nil),
				ValidateFunc: func(v interface{}, k string) (ws []string, errors []error) {
					value := v.(string)
					if value == "" {
						errors = append(errors, fmt.Errorf("endpoint must not be an empty string"))
					}

					return
				},
			},

			"username": {
				Type:        schema.TypeString,
				Description: "Username to use to authenticate with the server, can also be sourced from the `MYSQL_USERNAME` environment variable.",
				Required:    true,
				DefaultFunc: schema.EnvDefaultFunc("MYSQL_USERNAME", nil),
			},

			"password": {
				Type:        schema.TypeString,
				Description: "Password for the given user, if that user has a password, can also be sourced from the `MYSQL_PASSWORD` environment variable.",
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("MYSQL_PASSWORD", nil),
			},

			"proxy": {
				Type:        schema.TypeString,
				Description: "Proxy socks url, can also be sourced from `ALL_PROXY` or `all_proxy` environment variables.",
				Optional:    true,
				DefaultFunc: schema.MultiEnvDefaultFunc([]string{
					"ALL_PROXY",
					"all_proxy",
				}, nil),
				ValidateFunc: validation.StringMatch(regexp.MustCompile(`^socks5h?://.*:\d+$`), "The proxy URL is not a valid socks url."),
			},

			"tls": {
				Type:        schema.TypeString,
				Description: "The TLS configuration. One of `false`, `true`, or `skip-verify`. Defaults to `false`. Can also be sourced from the `MYSQL_TLS_CONFIG` environment variable.",
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("MYSQL_TLS_CONFIG", "false"),
				ValidateFunc: validation.StringInSlice([]string{
					"true",
					"false",
					"skip-verify",
				}, false),
			},

			"max_conn_lifetime_sec": {
				Type:        schema.TypeInt,
				Description: "Sets the maximum amount of time a connection may be reused. If d <= 0, connections are reused forever.",
				Optional:    true,
			},

			"max_open_conns": {
				Type:        schema.TypeInt,
				Description: "Sets the maximum number of open connections to the database. If n <= 0, then there is no limit on the number of open connections.",
				Optional:    true,
			},

			"authentication_plugin": {
				Type:         schema.TypeString,
				Description:  "Sets the authentication plugin, it can be one of the following: `native` or `cleartext`. Defaults to `native`.",
				Optional:     true,
				Default:      nativePasswords,
				ValidateFunc: validation.StringInSlice([]string{cleartextPasswords, nativePasswords}, true),
			},

			"aws_ssm_session_manager_client_config": {
				Type:        schema.TypeList,
				Optional:    true,
				MaxItems:    1,
				Description: "Configuration for use aws session manager.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"ec2_instance_id": {
							Type:        schema.TypeString,
							Description: "The EC2 server can connect the RDS to use. If you are managing by Terraform, you can set the value from `aws_instance`'s id.",
							Required:    true,
						},
						"rds_endpoint": {
							Type:        schema.TypeString,
							Description: "The endpoint of the RDS to use. If you are managing by Terraform, you can set the value from `aws_db_instance` or `aws_rds_cluster`'s endpoint.",
							Required:    true,
						},
						"ssh_user": {
							Type:        schema.TypeString,
							Description: "SSH user name. Defaults to current user name.",
							Optional:    true,
						},
						"ssh_key_path": {
							Type:        schema.TypeString,
							Description: "SSH user's private key path. Defaults to `~/.ssh/id_rsa`.",
							Optional:    true,
						},
						"aws_profile": {
							Type:        schema.TypeString,
							Description: "AWS user's profile (SSO logged in), can also be sourced from the `AWS_PROFILE` or `AWS_DEFAULT_PROFILE` environment variables. If you use AWS credentials, they can also be sourced from the `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY_ID`, and `AWS_SESSION_TOKEN` environment variables.",
							DefaultFunc: schema.MultiEnvDefaultFunc([]string{
								"AWS_PROFILE",
								"AWS_DEFAULT_PROFILE",
							}, ""),
							Optional: true,
						},
						"region": {
							Type:        schema.TypeString,
							Description: "AWS region, can also be sourced from the `AWS_REGION` or `AWS_DEFAULT_REGION` environment variables.",
							DefaultFunc: schema.MultiEnvDefaultFunc([]string{
								"AWS_REGION",
								"AWS_DEFAULT_REGION",
							}, ""),
							Optional: true,
						},
					},
				},
			},
			"port_forward_client_config": {
				Type:        schema.TypeList,
				Optional:    true,
				MaxItems:    1,
				Description: "Configuration for Port Forward.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"remote_host": {
							Type:        schema.TypeString,
							Description: "The IP or host of public bastion server can connect the DB server to use. May include a port as `host:port`; defaults to port 22.",
							Required:    true,
						},
						"db_endpoint": {
							Type:        schema.TypeString,
							Description: "The endpoint of the DB server to use.",
							Required:    true,
						},
						"ssh_user": {
							Type:        schema.TypeString,
							Description: "SSH user name. Defaults to current user name.",
							Optional:    true,
						},
						"ssh_key_path": {
							Type:        schema.TypeString,
							Description: "SSH user's private key path. Defaults to `~/.ssh/id_rsa`.",
							Optional:    true,
						},
					},
				},
			},
		},

		ResourcesMap: map[string]*schema.Resource{
			"mysql_database":      resourceDatabase(),
			"mysql_grant":         resourceGrant(),
			"mysql_role":          resourceRole(),
			"mysql_user":          resourceUser(),
			"mysql_user_password": resourceUserPassword(),
		},

		ConfigureContextFunc: providerConfigure,
	}
}

func providerConfigure(ctx context.Context, d *schema.ResourceData) (interface{}, diag.Diagnostics) {

	var endpoint = d.Get("endpoint").(string)

	proto := "tcp"
	if len(endpoint) > 0 && endpoint[0] == '/' {
		proto = "unix"
	}

	conf := mysql.Config{
		User:                    d.Get("username").(string),
		Passwd:                  d.Get("password").(string),
		Net:                     proto,
		Addr:                    endpoint,
		TLSConfig:               d.Get("tls").(string),
		AllowNativePasswords:    d.Get("authentication_plugin").(string) == nativePasswords,
		AllowCleartextPasswords: d.Get("authentication_plugin").(string) == cleartextPasswords,
	}

	dialer, err := makeDialer(d)
	if err != nil {
		return nil, diag.FromErr(err)
	}

	mysql.RegisterDialContext("tcp", func(ctx context.Context, addr string) (net.Conn, error) {
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, "tcp", addr)
		}
		return dialer.Dial("tcp", addr)
	})

	sessionConf, pfConfMap, err := port_forward.ParseSessionConfig(d)
	if err != nil {
		return nil, diag.FromErr(err)
	}
	if pfConfMap == nil {
		pfConfMap, err = port_forward.ParsePFConfigMap(d)
		if err != nil {
			return nil, diag.FromErr(err)
		}
	}
	var lp uint16
	if pfConfMap != nil {
		lp, err = localPortFromEndpoint(endpoint)
		if err != nil {
			return nil, diag.FromErr(err)
		}
	}
	pfConf, err := port_forward.ParsePFConfig(pfConfMap, lp)
	if err != nil {
		return nil, diag.FromErr(err)
	}

	// providerConfigure runs once per Terraform operation (and per acceptance
	// test step) in the same process. Reuse a tunnel already listening on the
	// same local port with the same settings instead of failing with
	// "address already in use".
	var tunnelKey string
	if pfConf != nil {
		tunnelKey = fmt.Sprintf("%d|%v", lp, pfConfMap)
	}
	var tunnel *port_forward.Tunnel
	if tunnelKey != "" {
		activeConfigsMu.Lock()
		for _, ac := range activeConfigs {
			if ac.tunnelKey == tunnelKey {
				tunnel = ac.tunnel
				break
			}
		}
		activeConfigsMu.Unlock()
	}
	if tunnel == nil {
		tunnel, err = port_forward.Connect(sessionConf, pfConf)
		if err != nil {
			return nil, diag.FromErr(err)
		}
	}

	mc := &MySQLConfiguration{
		tunnelKey:       tunnelKey,
		Config:          &conf,
		MaxConnLifetime: time.Duration(d.Get("max_conn_lifetime_sec").(int)) * time.Second,
		MaxOpenConns:    d.Get("max_open_conns").(int),
		tunnel:          tunnel,
	}
	activeConfigsMu.Lock()
	activeConfigs = append(activeConfigs, mc)
	activeConfigsMu.Unlock()

	return mc, nil
}

// localPortFromEndpoint extracts the local port from a host:port endpoint.
// It is only needed when port forwarding is enabled.
func localPortFromEndpoint(endpoint string) (uint16, error) {
	_, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return 0, fmt.Errorf("endpoint must be host:port when port forwarding is enabled (got %q): %v", endpoint, err)
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("endpoint must be host:port when port forwarding is enabled: invalid port %q in %q", portStr, endpoint)
	}
	return uint16(p), nil
}

func makeDialer(d *schema.ResourceData) (proxy.Dialer, error) {
	proxyFromEnv := proxy.FromEnvironment()
	proxyArg := d.Get("proxy").(string)

	if len(proxyArg) > 0 {
		proxyURL, err := url.Parse(proxyArg)
		if err != nil {
			return nil, err
		}
		proxy, err := proxy.FromURL(proxyURL, proxyFromEnv)
		if err != nil {
			return nil, err
		}

		return proxy, nil
	}

	return proxyFromEnv, nil
}

func serverVersion(db *sql.DB) (*version.Version, error) {
	var versionString string
	err := db.QueryRow("SELECT @@GLOBAL.innodb_version").Scan(&versionString)
	if err != nil {
		return nil, err
	}

	return version.NewVersion(versionString)
}

func serverVersionString(db *sql.DB) (string, error) {
	var versionString string
	err := db.QueryRow("SELECT @@GLOBAL.version").Scan(&versionString)
	if err != nil {
		return "", err
	}

	return versionString, nil
}

func connectToMySQL(ctx context.Context, conf *MySQLConfiguration) (*sql.DB, error) {
	conf.mu.Lock()
	defer conf.mu.Unlock()

	if conf.db != nil {
		return conf.db, nil
	}

	dsn := conf.Config.FormatDSN()
	var db *sql.DB

	// When provisioning a database server there can often be a lag between
	// when Terraform thinks it's available and when it is actually available.
	// This is particularly acute when provisioning a server and then immediately
	// trying to provision a database on it.
	retryError := retry.RetryContext(ctx, 5*time.Minute, func() *retry.RetryError {
		var err error
		db, err = sql.Open("mysql", dsn)
		if err != nil {
			return retry.RetryableError(err)
		}

		if err = db.PingContext(ctx); err != nil {
			db.Close()
			return retry.RetryableError(err)
		}

		return nil
	})

	if retryError != nil {
		return nil, fmt.Errorf("could not connect to server: %s", retryError)
	}
	db.SetConnMaxLifetime(conf.MaxConnLifetime)
	db.SetMaxOpenConns(conf.MaxOpenConns)
	conf.db = db
	return db, nil
}
