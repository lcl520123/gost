package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	mdutil "github.com/go-gost/core/metadata/util"
	"github.com/go-gost/x/config"
	xlimiter "github.com/go-gost/x/limiter"
	mdx "github.com/go-gost/x/metadata"
	"github.com/go-gost/x/registry"
)

var (
	ErrInvalidCmd  = errors.New("invalid cmd")
	ErrInvalidNode = errors.New("invalid node")
)

type stringList []string

func (l *stringList) String() string {
	return fmt.Sprintf("%s", *l)
}
func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

func buildConfigFromCmd(services, nodes stringList) (*config.Config, error) {
	cfg := &config.Config{}

	if v := os.Getenv("GOST_PROFILING"); v != "" {
		cfg.Profiling = &config.ProfilingConfig{
			Addr: v,
		}
	}
	if v := os.Getenv("GOST_METRICS"); v != "" {
		cfg.Metrics = &config.MetricsConfig{
			Addr: v,
		}
	}

	if v := os.Getenv("GOST_LOGGER_LEVEL"); v != "" {
		cfg.Log = &config.LogConfig{
			Level: v,
		}
	}

	if v := os.Getenv("GOST_API"); v != "" {
		cfg.API = &config.APIConfig{
			Addr: v,
		}
	}

	var chain *config.ChainConfig
	if len(nodes) > 0 {
		chain = &config.ChainConfig{
			Name: "chain-0",
		}
		cfg.Chains = append(cfg.Chains, chain)
	}

	for i, node := range nodes {
		url, err := normCmd(node)
		if err != nil {
			return nil, err
		}

		nodeConfig, err := buildNodeConfig(url)
		if err != nil {
			return nil, err
		}
		nodeConfig.Name = "node-0"

		var nodes []*config.NodeConfig
		for _, host := range strings.Split(nodeConfig.Addr, ",") {
			if host == "" {
				continue
			}
			nodeCfg := &config.NodeConfig{}
			*nodeCfg = *nodeConfig
			nodeCfg.Name = fmt.Sprintf("node-%d", len(nodes))
			nodeCfg.Addr = host
			nodes = append(nodes, nodeCfg)
		}

		mc := nodeConfig.Connector.Metadata
		md := mdx.NewMetadata(mc)

		hopConfig := &config.HopConfig{
			Name:     fmt.Sprintf("hop-%d", i),
			Selector: parseSelector(mc),
			Nodes:    nodes,
		}

		if v := mdutil.GetString(md, "bypass"); v != "" {
			bypassCfg := &config.BypassConfig{
				Name: fmt.Sprintf("bypass-%d", len(cfg.Bypasses)),
			}
			if v[0] == '~' {
				bypassCfg.Whitelist = true
				v = v[1:]
			}
			for _, s := range strings.Split(v, ",") {
				if s == "" {
					continue
				}
				bypassCfg.Matchers = append(bypassCfg.Matchers, s)
			}
			hopConfig.Bypass = bypassCfg.Name
			cfg.Bypasses = append(cfg.Bypasses, bypassCfg)
			delete(mc, "bypass")
		}
		if v := mdutil.GetString(md, "resolver"); v != "" {
			resolverCfg := &config.ResolverConfig{
				Name: fmt.Sprintf("resolver-%d", len(cfg.Resolvers)),
			}
			for _, rs := range strings.Split(v, ",") {
				if rs == "" {
					continue
				}
				resolverCfg.Nameservers = append(
					resolverCfg.Nameservers,
					&config.NameserverConfig{
						Addr: rs,
					},
				)
			}
			hopConfig.Resolver = resolverCfg.Name
			cfg.Resolvers = append(cfg.Resolvers, resolverCfg)
			delete(mc, "resolver")
		}
		if v := mdutil.GetString(md, "hosts"); v != "" {
			hostsCfg := &config.HostsConfig{
				Name: fmt.Sprintf("hosts-%d", len(cfg.Hosts)),
			}
			for _, s := range strings.Split(v, ",") {
				ss := strings.SplitN(s, ":", 2)
				if len(ss) != 2 {
					continue
				}
				hostsCfg.Mappings = append(
					hostsCfg.Mappings,
					&config.HostMappingConfig{
						Hostname: ss[0],
						IP:       ss[1],
					},
				)
			}
			hopConfig.Hosts = hostsCfg.Name
			cfg.Hosts = append(cfg.Hosts, hostsCfg)
			delete(mc, "hosts")
		}

		if v := mdutil.GetString(md, "interface"); v != "" {
			hopConfig.Interface = v
			delete(mc, "interface")
		}
		if v := mdutil.GetInt(md, "so_mark"); v > 0 {
			hopConfig.SockOpts = &config.SockOptsConfig{
				Mark: v,
			}
			delete(mc, "so_mark")
		}

		chain.Hops = append(chain.Hops, hopConfig)
	}

	for i, svc := range services {
		url, err := normCmd(svc)
		if err != nil {
			return nil, err
		}

		service, err := buildServiceConfig(url)
		if err != nil {
			return nil, err
		}
		service.Name = fmt.Sprintf("service-%d", i)
		if chain != nil {
			if service.Listener.Type == "rtcp" || service.Listener.Type == "rudp" {
				service.Listener.Chain = chain.Name
			} else {
				service.Handler.Chain = chain.Name
			}
		}
		cfg.Services = append(cfg.Services, service)

		mh := service.Handler.Metadata
		md := mdx.NewMetadata(mh)
		if v := mdutil.GetInt(md, "retries"); v > 0 {
			service.Handler.Retries = v
			delete(mh, "retries")
		}
		if v := mdutil.GetString(md, "admission"); v != "" {
			admCfg := &config.AdmissionConfig{
				Name: fmt.Sprintf("admission-%d", len(cfg.Admissions)),
			}
			if v[0] == '~' {
				admCfg.Whitelist = true
				v = v[1:]
			}
			for _, s := range strings.Split(v, ",") {
				if s == "" {
					continue
				}
				admCfg.Matchers = append(admCfg.Matchers, s)
			}
			service.Admission = admCfg.Name
			cfg.Admissions = append(cfg.Admissions, admCfg)
			delete(mh, "admission")
		}
		if v := mdutil.GetString(md, "bypass"); v != "" {
			bypassCfg := &config.BypassConfig{
				Name: fmt.Sprintf("bypass-%d", len(cfg.Bypasses)),
			}
			if v[0] == '~' {
				bypassCfg.Whitelist = true
				v = v[1:]
			}
			for _, s := range strings.Split(v, ",") {
				if s == "" {
					continue
				}
				bypassCfg.Matchers = append(bypassCfg.Matchers, s)
			}
			service.Bypass = bypassCfg.Name
			cfg.Bypasses = append(cfg.Bypasses, bypassCfg)
			delete(mh, "bypass")
		}
		if v := mdutil.GetString(md, "resolver"); v != "" {
			resolverCfg := &config.ResolverConfig{
				Name: fmt.Sprintf("resolver-%d", len(cfg.Resolvers)),
			}
			for _, rs := range strings.Split(v, ",") {
				if rs == "" {
					continue
				}
				resolverCfg.Nameservers = append(
					resolverCfg.Nameservers,
					&config.NameserverConfig{
						Addr:   rs,
						Prefer: mdutil.GetString(md, "prefer"),
					},
				)
			}
			service.Resolver = resolverCfg.Name
			cfg.Resolvers = append(cfg.Resolvers, resolverCfg)
			delete(mh, "resolver")
		}
		if v := mdutil.GetString(md, "hosts"); v != "" {
			hostsCfg := &config.HostsConfig{
				Name: fmt.Sprintf("hosts-%d", len(cfg.Hosts)),
			}
			for _, s := range strings.Split(v, ",") {
				ss := strings.SplitN(s, ":", 2)
				if len(ss) != 2 {
					continue
				}
				hostsCfg.Mappings = append(
					hostsCfg.Mappings,
					&config.HostMappingConfig{
						Hostname: ss[0],
						IP:       ss[1],
					},
				)
			}
			service.Hosts = hostsCfg.Name
			cfg.Hosts = append(cfg.Hosts, hostsCfg)
			delete(mh, "hosts")
		}

		in := mdutil.GetString(md, "limiter.rate.in")
		out := mdutil.GetString(md, "limiter.rate.out")
		cin := mdutil.GetString(md, "limiter.rate.conn.in")
		cout := mdutil.GetString(md, "limiter.rate.conn.out")
		if in != "" || cin != "" {
			limiter := &config.LimiterConfig{
				Name: fmt.Sprintf("limiter-%d", len(cfg.Limiters)),
				Rate: &config.RateLimiterConfig{},
			}
			if in != "" {
				limiter.Rate.Limits = append(limiter.Rate.Limits,
					fmt.Sprintf("%s %s %s", xlimiter.GlobalLimitKey, in, out))
			}
			if cin != "" {
				limiter.Rate.Limits = append(limiter.Rate.Limits,
					fmt.Sprintf("%s %s %s", xlimiter.ConnLimitKey, cin, cout))
			}
			service.Limiter = limiter.Name
			cfg.Limiters = append(cfg.Limiters, limiter)
			delete(mh, "limiter.rate.in")
			delete(mh, "limiter.rate.out")
			delete(mh, "limiter.rate.conn.in")
			delete(mh, "limiter.rate.conn.out")
		}
	}

	return cfg, nil
}

func buildServiceConfig(url *url.URL) (*config.ServiceConfig, error) {
	var handler, listener string
	schemes := strings.Split(url.Scheme, "+")
	if len(schemes) == 1 {
		handler = schemes[0]
		listener = schemes[0]
	}
	if len(schemes) == 2 {
		handler = schemes[0]
		listener = schemes[1]
	}

	svc := &config.ServiceConfig{
		Addr: url.Host,
	}

	if h := registry.HandlerRegistry().Get(handler); h == nil {
		handler = "auto"
	}
	if ln := registry.ListenerRegistry().Get(listener); ln == nil {
		listener = "tcp"
		if handler == "ssu" {
			listener = "udp"
		}
	}

	// forward mode
	if remotes := strings.Trim(url.EscapedPath(), "/"); remotes != "" {
		svc.Forwarder = &config.ForwarderConfig{
			// Targets: strings.Split(remotes, ","),
		}
		for i, addr := range strings.Split(remotes, ",") {
			svc.Forwarder.Nodes = append(svc.Forwarder.Nodes,
				&config.NodeConfig{
					Name: fmt.Sprintf("target-%d", i),
					Addr: addr,
				})
		}
		if handler != "relay" {
			if listener == "tcp" || listener == "udp" ||
				listener == "rtcp" || listener == "rudp" ||
				listener == "tun" || listener == "tap" ||
				listener == "dns" {
				handler = listener
			} else {
				handler = "forward"
			}
		}
	}

	var auth *config.AuthConfig
	if url.User != nil {
		auth = &config.AuthConfig{
			Username: url.User.Username(),
		}
		auth.Password, _ = url.User.Password()
	}

	m := map[string]any{}
	for k, v := range url.Query() {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	md := mdx.NewMetadata(m)

	if sa := mdutil.GetString(md, "auth"); sa != "" {
		au, err := parseAuthFromCmd(sa)
		if err != nil {
			return nil, err
		}
		auth = au
	}
	delete(m, "auth")

	tlsConfig := &config.TLSConfig{
		CertFile: mdutil.GetString(md, "certFile"),
		KeyFile:  mdutil.GetString(md, "keyFile"),
		CAFile:   mdutil.GetString(md, "caFile"),
	}
	if tlsConfig.CertFile == "" {
		tlsConfig.CertFile = mdutil.GetString(md, "cert")
	}
	if tlsConfig.KeyFile == "" {
		tlsConfig.KeyFile = mdutil.GetString(md, "key")
	}
	if tlsConfig.CAFile == "" {
		tlsConfig.CAFile = mdutil.GetString(md, "ca")
	}

	delete(m, "certFile")
	delete(m, "cert")
	delete(m, "keyFile")
	delete(m, "key")
	delete(m, "caFile")
	delete(m, "ca")

	if tlsConfig.CertFile == "" {
		tlsConfig = nil
	}

	if v := mdutil.GetString(md, "dns"); v != "" {
		md.Set("dns", strings.Split(v, ","))
	}

	if svc.Forwarder != nil {
		svc.Forwarder.Selector = parseSelector(m)
	}

	svc.Handler = &config.HandlerConfig{
		Type:     handler,
		Auth:     auth,
		Metadata: m,
	}
	svc.Listener = &config.ListenerConfig{
		Type:     listener,
		TLS:      tlsConfig,
		Metadata: m,
	}

	svc.Metadata = m

	if svc.Listener.Type == "ssh" || svc.Listener.Type == "sshd" {
		svc.Handler.Auth = nil
		svc.Listener.Auth = auth
	}

	return svc, nil
}

func buildNodeConfig(url *url.URL) (*config.NodeConfig, error) {
	var connector, dialer string
	schemes := strings.Split(url.Scheme, "+")
	if len(schemes) == 1 {
		connector = schemes[0]
		dialer = schemes[0]
	}
	if len(schemes) == 2 {
		connector = schemes[0]
		dialer = schemes[1]
	}

	node := &config.NodeConfig{
		Addr: url.Host,
	}

	if c := registry.ConnectorRegistry().Get(connector); c == nil {
		connector = "http"
	}
	if d := registry.DialerRegistry().Get(dialer); d == nil {
		dialer = "tcp"
		if connector == "ssu" {
			dialer = "udp"
		}
	}

	var auth *config.AuthConfig
	if url.User != nil {
		auth = &config.AuthConfig{
			Username: url.User.Username(),
		}
		auth.Password, _ = url.User.Password()
	}

	m := map[string]any{}
	for k, v := range url.Query() {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	md := mdx.NewMetadata(m)

	if sauth := mdutil.GetString(md, "auth"); sauth != "" && auth == nil {
		au, err := parseAuthFromCmd(sauth)
		if err != nil {
			return nil, err
		}
		auth = au
	}
	delete(m, "auth")

	tlsConfig := &config.TLSConfig{
		CertFile:   mdutil.GetString(md, "certFile"),
		KeyFile:    mdutil.GetString(md, "keyFile"),
		CAFile:     mdutil.GetString(md, "caFile"),
		Secure:     mdutil.GetBool(md, "secure"),
		ServerName: mdutil.GetString(md, "serverName"),
	}
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName = url.Hostname()
	}
	if tlsConfig.CertFile == "" {
		tlsConfig.CertFile = mdutil.GetString(md, "cert")
	}
	if tlsConfig.KeyFile == "" {
		tlsConfig.KeyFile = mdutil.GetString(md, "key")
	}
	if tlsConfig.CAFile == "" {
		tlsConfig.CAFile = mdutil.GetString(md, "ca")
	}

	delete(m, "certFile")
	delete(m, "cert")
	delete(m, "keyFile")
	delete(m, "key")
	delete(m, "caFile")
	delete(m, "ca")
	delete(m, "secure")
	delete(m, "serverName")

	if !tlsConfig.Secure && tlsConfig.CertFile == "" && tlsConfig.CAFile == "" {
		tlsConfig = nil
	}

	node.Connector = &config.ConnectorConfig{
		Type:     connector,
		Auth:     auth,
		Metadata: m,
	}
	node.Dialer = &config.DialerConfig{
		Type:     dialer,
		TLS:      tlsConfig,
		Metadata: m,
	}

	if node.Dialer.Type == "ssh" || node.Dialer.Type == "sshd" {
		node.Connector.Auth = nil
		node.Dialer.Auth = auth
	}

	return node, nil
}

func normCmd(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ErrInvalidCmd
	}

	if s[0] == ':' || !strings.Contains(s, "://") {
		s = "auto://" + s
	}

	url, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	if url.Scheme == "https" {
		url.Scheme = "http+tls"
	}

	return url, nil
}

func parseAuthFromCmd(sa string) (*config.AuthConfig, error) {
	v, err := base64.StdEncoding.DecodeString(sa)
	if err != nil {
		return nil, err
	}
	cs := string(v)
	n := strings.IndexByte(cs, ':')
	if n < 0 {
		return &config.AuthConfig{
			Username: cs,
		}, nil
	}

	return &config.AuthConfig{
		Username: cs[:n],
		Password: cs[n+1:],
	}, nil
}

func parseSelector(m map[string]any) *config.SelectorConfig {
	md := mdx.NewMetadata(m)
	strategy := mdutil.GetString(md, "strategy")
	maxFails := mdutil.GetInt(md, "maxFails")
	if maxFails == 0 {
		maxFails = mdutil.GetInt(md, "max_fails")
	}
	failTimeout := mdutil.GetDuration(md, "failTimeout")
	if failTimeout == 0 {
		failTimeout = mdutil.GetDuration(md, "fail_timeout")
	}
	if strategy == "" && maxFails <= 0 && failTimeout <= 0 {
		return nil
	}
	if strategy == "" {
		strategy = "round"
	}
	if maxFails <= 0 {
		maxFails = 1
	}
	if failTimeout <= 0 {
		failTimeout = 30 * time.Second
	}

	delete(m, "strategy")
	delete(m, "maxFails")
	delete(m, "max_fails")
	delete(m, "failTimeout")
	delete(m, "fail_timeout")

	return &config.SelectorConfig{
		Strategy:    strategy,
		MaxFails:    maxFails,
		FailTimeout: failTimeout,
	}
}
