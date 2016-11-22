package router

import (
	"fmt"
	"github.com/IMQS/serviceconfigsgo"
	"net/url"
	"strings"
)

/*
Example configuration file:

{
	"Proxy": "http://192.168.1.1:1234",							This is used to route any targets that specify UseProxy: true
	"AccessLog": "c:/imqsvar/logs/router-access.log",			The access log file. If empty, defaults to 'stdout'.
	"ErrorLog": "c:/imqsvar/logs/router-error.log",				The error log file. If empty, defaults to 'stderr'.
	"LogLevel": "info",											The log level of the error log. Defaults to "info". Valid values are "trace", "debug", "info", "warn", "error"
	"DebugRoutes": true,										Log every match attempt to the error log.
	"HTTP": {
		"Port": 80,												Primary HTTP port
		"SecondaryPort": 8080,									One can optionally listen for HTTP on two ports
		"EnableHTTPS": true,									Enable HTTPS
		"RedirectHTTP": true,									Redirect any domain request to HTTP(port 80) to use HTTPS(port 443) to force secure connection to user
		"HTTPSPort": 444,										Override default HTTPS port (443)
		"CertKeyFile": "c:/imqsbin/conf/ssl.key"				SSL private key
		"CertFile": "c:/imqsbin/conf/ssl.crt"					SSL certificate file. Concatenation of your certificate with the CA certificate chain.
		"DisableKeepAlive": true,								Controls http.Transport.DisableKeepAlive (backend comms). Default = false
		"MaxIdleConnections": 50,								Controls http.Transport.MaxIdleConnections (backend comms). Default = 0 (uses Go std library default)
		"ResponseHeaderTimeout": 60								Controls http.Transport.ResponseHeaderTimeout (backend comms). Default = 0 (uses Go std library default)
	},
	"Targets": {
		"MAPS": {												Targets names must be CAPITAL. This rule exists solely to enforce a convention.
			"URL": "http://127.0.0.1:2000",
			"UseProxy": true									If true, and a proxy is specified, then route this traffic through the proxy
		},
		"THIRDPARTY": {
			"URL": "https://externalsite.com",
			"RequirePermission": "enabled",						Do not allow traffic to this target unless imqsauth says we have this permission
			"PassThroughAuth": {								Transparent authentication
				"Type": "PureHub",
				"LoginURL": "https://externalsite.com/Token",
				"Username": "username@example.com",
				"Password": "mypassword"
			}
		},
		"YELLOWFIN": {											Demonstrates the "Yellowfin" transparent authentication system
			"URL": "http://yellowfinserver.example.com",
			"RequirePermission": "enabled",
			"PassThroughAuth": {
				"Type": "Yellowfin"
			}
		}
	},
	"Routes": {
		"/tile/(.*)": "{MAPS}/tile/$1",							Left side is a regex matcher. Right side is replacement.
		"/themes/(.*)": "{MAPS}/theme/$1",						If you use a named target, like {MAPS}, then it must be the first part of the replacement string.
		"/docs/(.*)": "https://docs.example.com/$1",
		"/about/(.*)": "http://127.0.0.1:2001/$1",
		"/3rdparty/(.*)": "{THIRDPARTY}/$1",					Transparent authentication to PureHub
		"/yellowfin/(.*)": "{YELLOWFIN}/$1",					Transparent authentication to Yellowfin
		"/telemetry/(.*)": "ws://127.0.0.1:2001/$1",			Websocket target
		"/crud/(.*)": "httpbridge://2013/$1",					HttpBridge on port 2013. Note that no host is specified - only the port (2013 in this example).
		"/(.*)": "http://127.0.0.1/www/$1"						This will end up catching anything that doesn't match one of the more specific routes
	},
}

Notes about configuration:
In order to keep the system performant, routes must start with a static prefix. The first opening parenthesis
signals the end of the prefix. Should we need more complicated rewriting rules, we'd need to add support for that.
At present the route matching is actually based purely on a hash table lookup of the prefix. The regex replacement
is performed as one would assume, but that is only after a particular route has been chosen. The maximum depth,
in terms of the number of slashes in the prefix, is 10. In other words prefixes beyond /a/b/c/d/(.*) won't work correctly.
*/

type AuthPassThroughType string

const (
	AuthPassThroughNone      AuthPassThroughType = ""
	AuthPassThroughPureHub                       = "PureHub"
	AuthPassThroughYellowfin                     = "Yellowfin"
	AuthPassThroughSitePro                       = "SitePro"
	AuthPassThroughECS                           = "ECS"
	serviceConfigFileName                        = "router-config.json"
	serviceConfigVersion                         = 1
	serviceName                                  = "ImqsRouter"
)

type Config struct {
	Proxy       string
	AccessLog   string
	ErrorLog    string
	LogLevel    string
	DebugRoutes bool
	HTTP        ConfigHTTP
	Targets     map[string]ConfigTarget
	Routes      map[string]string
}

type ConfigHTTP struct {
	Port                  uint16
	SecondaryPort         uint16
	HTTPSPort             uint16
	EnableHTTPS           bool
	CertFile              string
	CertKeyFile           string
	DisableKeepAlive      bool
	MaxIdleConnections    int
	ResponseHeaderTimeout int
	RedirectHTTP          bool
}

type ConfigPassThroughAuth struct {
	Type     AuthPassThroughType
	LoginURL string
	Username string
	Password string
}

type ConfigTarget struct {
	URL               string
	UseProxy          bool
	RequirePermission string
	PassThroughAuth   ConfigPassThroughAuth
}

// {FOO}/bar -> (FOO, /bar)
func split_named_target(targetURL string) (string, string) {
	open := strings.Index(targetURL, "{")
	close := strings.Index(targetURL, "}")
	if open != 0 || close < 1 {
		return "", ""
	}
	return targetURL[open+1 : close], targetURL[close+1:]
}

func (h *ConfigHTTP) GetPort() uint16 {
	if h.Port == 0 {
		return 80
	}
	return h.Port
}

func (c *Config) Reset() {
	*c = Config{}
	c.Targets = make(map[string]ConfigTarget)
	c.Routes = make(map[string]string)
}

// Return nil if the configuration passes sanity and integrity checks
func (c *Config) verify() error {
	for match, replace := range c.Routes {
		if len(match) == 0 || match[0] != '/' {
			return fmt.Errorf("Match must start with '/' (%v -> %v)", match, replace)
		}
		if len(replace) == 0 {
			return fmt.Errorf("Replacement URL (%v -> %v) may not be empty", match, replace)
		}

		if replace[0] == '{' {
			named_target, _ := split_named_target(replace)
			if named_target == "" {
				return fmt.Errorf("URL target format (%v) not recognized", replace)
			} else {
				if _, exist := c.Targets[named_target]; !exist {
					return fmt.Errorf("URL target %v not defined", named_target)
				}
			}
		} else if parse_scheme(replace) == scheme_unknown {
			return fmt.Errorf("Unrecognized URL scheme (%v). Must be one of http://, https://, ws://, httpbridge://, {TARGET}", replace)
		}
	}
	for name, target := range c.Targets {
		if strings.ToUpper(name) != name {
			return fmt.Errorf("Target names must be upper case (%v)", name)
		}
		if parse_scheme(target.URL) == scheme_unknown {
			return fmt.Errorf("Unrecognized URL scheme (%v). Must be one of http://, https://, ws://, httpbridge://", target.URL)
		}
	}
	if c.Proxy != "" {
		_, err := url.Parse(c.Proxy)
		if err != nil {
			return fmt.Errorf("Could not parse proxy URL (%v): %v", c.Proxy, err)
		}
	}
	return nil
}

func (c *Config) LoadFile(filename string) error {
	c.Reset()
	err := serviceconfig.GetConfig(filename, serviceName, serviceConfigVersion, serviceConfigFileName, c)
	if err != nil {
		return err
	}
	return c.verify()
}

// Overlay 'other' on top of this configuration
// We lack a perfect notion of 'defined'. For example, DisableKeepAlive is a bool,
// so we don't know whether it was defined in the JSON or not. This is OK for us,
// since the only thing we currently need to overlay is Proxy
func (c *Config) Overlay(other *Config) {
	if other.Proxy != "" {
		c.Proxy = other.Proxy
	}

	// Logs
	if other.AccessLog != "" {
		c.AccessLog = other.AccessLog
	}
	if other.ErrorLog != "" {
		c.ErrorLog = other.ErrorLog
	}
	if other.LogLevel != "" {
		c.LogLevel = other.LogLevel
	}

	if other.DebugRoutes {
		c.DebugRoutes = other.DebugRoutes
	}

	// HTTP
	if other.HTTP.Port != 0 {
		c.HTTP.Port = other.HTTP.Port
	}
	if other.HTTP.SecondaryPort != 0 {
		c.HTTP.SecondaryPort = other.HTTP.SecondaryPort
	}
	if other.HTTP.HTTPSPort != 0 {
		c.HTTP.HTTPSPort = other.HTTP.HTTPSPort
	}
	if other.HTTP.EnableHTTPS {
		c.HTTP.EnableHTTPS = other.HTTP.EnableHTTPS
	}
	if other.HTTP.RedirectHTTP {
		c.HTTP.RedirectHTTP = other.HTTP.RedirectHTTP
	}
	if other.HTTP.CertFile != "" {
		c.HTTP.CertFile = other.HTTP.CertFile
	}
	if other.HTTP.CertKeyFile != "" {
		c.HTTP.CertKeyFile = other.HTTP.CertKeyFile
	}
	if other.HTTP.DisableKeepAlive {
		c.HTTP.DisableKeepAlive = other.HTTP.DisableKeepAlive
	}
	if other.HTTP.MaxIdleConnections != 0 {
		c.HTTP.MaxIdleConnections = other.HTTP.MaxIdleConnections
	}
	if other.HTTP.ResponseHeaderTimeout != 0 {
		c.HTTP.ResponseHeaderTimeout = other.HTTP.ResponseHeaderTimeout
	}

	for match, replace := range other.Routes {
		c.Routes[match] = replace
	}
	for name, cfg := range other.Targets {
		c.Targets[name] = cfg
	}
}
