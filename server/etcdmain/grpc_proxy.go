// Copyright 2016 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package etcdmain

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/logutil"
	"go.etcd.io/etcd/client/pkg/v3/tlsutil"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"
	"go.etcd.io/etcd/client/v3/namespace"
	"go.etcd.io/etcd/client/v3/ordering"
	"go.etcd.io/etcd/pkg/v3/debugutil"
	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v3election/v3electionpb"
	"go.etcd.io/etcd/server/v3/etcdserver/api/v3lock/v3lockpb"
	"go.etcd.io/etcd/server/v3/proxy/grpcproxy"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/soheilhy/cmux"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapgrpc"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/keepalive"
)

var (
	grpcProxyListenAddr                string
	grpcProxyMetricsListenAddr         string
	grpcProxyEndpoints                 []string
	grpcProxyEndpointsAutoSyncInterval time.Duration
	grpcProxyDNSCluster                string
	grpcProxyDNSClusterServiceName     string
	grpcProxyInsecureDiscovery         bool
	grpcProxyDataDir                   string
	grpcMaxCallSendMsgSize             int
	grpcMaxCallRecvMsgSize             int

	// tls for connecting to etcd

	grpcProxyCA                    string
	grpcProxyCert                  string
	grpcProxyKey                   string
	grpcProxyInsecureSkipTLSVerify bool

	// tls for clients connecting to proxy

	grpcProxyListenCA           string
	grpcProxyListenCert         string
	grpcProxyListenKey          string
	grpcProxyListenCipherSuites []string
	grpcProxyListenAutoTLS      bool
	grpcProxyListenCRL          string
	selfSignedCertValidity      uint

	grpcProxyAdvertiseClientURL string
	grpcProxyResolverPrefix     string
	grpcProxyResolverTTL        int

	grpcProxyNamespace string
	grpcProxyLeasing   string

	grpcProxyEnablePprof    bool
	grpcProxyEnableOrdering bool

	grpcProxyDebug bool

	// GRPC keep alive related options.
	grpcKeepAliveMinTime  time.Duration
	grpcKeepAliveTimeout  time.Duration
	grpcKeepAliveInterval time.Duration

	maxConcurrentStreams uint32
)

const defaultGRPCMaxCallSendMsgSize = 1.5 * 1024 * 1024

func init() {
	rootCmd.AddCommand(newGRPCProxyCommand())
}

// newGRPCProxyCommand returns the cobra command for "grpc-proxy".
func newGRPCProxyCommand() *cobra.Command {
	lpc := &cobra.Command{
		Use:   "grpc-proxy <subcommand>",
		Short: "grpc-proxy related command",
	}
	lpc.AddCommand(newGRPCProxyStartCommand())

	return lpc
}

func newGRPCProxyStartCommand() *cobra.Command {
	cmd := cobra.Command{
		Use:   "start",
		Short: "start the grpc proxy",
		Run:   startGRPCProxy, // 启动 etcd proxy 的入口方法
	}

	// etcd-proxy 的端口，gRPC 和 HTTP 服务都会监听这个端口
	cmd.Flags().StringVar(&grpcProxyListenAddr, "listen-addr", "127.0.0.1:23790", "listen address")
	cmd.Flags().StringVar(&grpcProxyDNSCluster, "discovery-srv", "", "domain name to query for SRV records describing cluster endpoints")
	cmd.Flags().StringVar(&grpcProxyDNSClusterServiceName, "discovery-srv-name", "", "service name to query when using DNS discovery")
	cmd.Flags().StringVar(&grpcProxyMetricsListenAddr, "metrics-addr", "", "listen for endpoint /metrics requests on an additional interface")
	cmd.Flags().BoolVar(&grpcProxyInsecureDiscovery, "insecure-discovery", false, "accept insecure SRV records")
	// etcd 集群的地址，proxy 会将请求转发到这些地址
	cmd.Flags().StringSliceVar(&grpcProxyEndpoints, "endpoints", []string{"127.0.0.1:12379", "127.0.0.1:22379", "127.0.0.1:32379"}, "comma separated etcd cluster endpoints")
	// proxy 自动更新 ETCD 集群成员信息的间隔，默认不自动更新（服务发现）
	cmd.Flags().DurationVar(&grpcProxyEndpointsAutoSyncInterval, "endpoints-auto-sync-interval", 0, "etcd endpoints auto sync interval (disabled by default)")
	// proxy 自身的地址，用于做自我健康检查使用
	cmd.Flags().StringVar(&grpcProxyAdvertiseClientURL, "advertise-client-url", "127.0.0.1:23790", "advertise address to register (must be reachable by client)")
	// 用于注册代理的前缀（必须与其他 grpc-proxy 成员共享）
	cmd.Flags().StringVar(&grpcProxyResolverPrefix, "resolver-prefix", "", "prefix to use for registering proxy (must be shared with other grpc-proxy members)")
	cmd.Flags().IntVar(&grpcProxyResolverTTL, "resolver-ttl", 0, "specify TTL, in seconds, when registering proxy endpoints")
	// proxy 处理的数据的默认前缀
	cmd.Flags().StringVar(&grpcProxyNamespace, "namespace", "/onemeta", "string to prefix to all keys for namespacing requests")
	// 是否允许导出 pprof 文件，用于排查线上问题。默认为 false
	cmd.Flags().BoolVar(&grpcProxyEnablePprof, "enable-pprof", true, `Enable runtime profiling data via HTTP server. Address is at client URL + "/debug/pprof/"`)
	// proxy 的持久化数据存放地方，只有开启 TLS 时候会使用
	cmd.Flags().StringVar(&grpcProxyDataDir, "data-dir", "default.proxy", "Data directory for persistent data")
	// 允许 client 发送的最大数据大小，默认为 1.5 MiB
	cmd.Flags().IntVar(&grpcMaxCallSendMsgSize, "max-send-bytes", defaultGRPCMaxCallSendMsgSize, "message send limits in bytes (default value is 1.5 MiB)")
	// 允许 etcd-server 返回的最大数据大小，默认不限制大小
	cmd.Flags().IntVar(&grpcMaxCallRecvMsgSize, "max-recv-bytes", math.MaxInt32, "message receive limits in bytes (default value is math.MaxInt32)")
	// 客户端在 ping 代理之前应等待的最小间隔长度，默认 5 秒
	cmd.Flags().DurationVar(&grpcKeepAliveMinTime, "grpc-keepalive-min-time", embed.DefaultGRPCKeepAliveMinTime, "Minimum interval duration that a client should wait before pinging proxy.")
	// 服务器到客户端的 ping 频率，以检查连接是否存活（0 表示禁用），默认 2 小时
	cmd.Flags().DurationVar(&grpcKeepAliveInterval, "grpc-keepalive-interval", embed.DefaultGRPCKeepAliveInterval, "Frequency duration of server-to-client ping to check if a connection is alive (0 to disable).")
	// 在关闭非响应连接之前等待的额外持续时间（0 表示禁用），默认 20 秒
	cmd.Flags().DurationVar(&grpcKeepAliveTimeout, "grpc-keepalive-timeout", embed.DefaultGRPCKeepAliveTimeout, "Additional duration of wait before closing a non-responsive connection (0 to disable).")

	// proxy->etcd-server 数据加密相关参数
	// client TLS for connecting to server
	cmd.Flags().StringVar(&grpcProxyCert, "cert", "", "identify secure connections with etcd servers using this TLS certificate file")
	cmd.Flags().StringVar(&grpcProxyKey, "key", "", "identify secure connections with etcd servers using this TLS key file")
	cmd.Flags().StringVar(&grpcProxyCA, "cacert", "", "verify certificates of TLS-enabled secure etcd servers using this CA bundle")
	cmd.Flags().BoolVar(&grpcProxyInsecureSkipTLSVerify, "insecure-skip-tls-verify", false, "skip authentication of etcd server TLS certificates (CAUTION: this option should be enabled only for testing purposes)")

	// client->proxy 数据加密相关参数
	// client TLS for connecting to proxy
	cmd.Flags().StringVar(&grpcProxyListenCert, "cert-file", "", "identify secure connections to the proxy using this TLS certificate file")
	cmd.Flags().StringVar(&grpcProxyListenKey, "key-file", "", "identify secure connections to the proxy using this TLS key file")
	cmd.Flags().StringVar(&grpcProxyListenCA, "trusted-ca-file", "", "verify certificates of TLS-enabled secure proxy using this CA bundle")
	cmd.Flags().StringSliceVar(&grpcProxyListenCipherSuites, "listen-cipher-suites", grpcProxyListenCipherSuites, "Comma-separated list of supported TLS cipher suites between client/proxy (empty will be auto-populated by Go).")
	cmd.Flags().BoolVar(&grpcProxyListenAutoTLS, "auto-tls", false, "proxy TLS using generated certificates")
	cmd.Flags().StringVar(&grpcProxyListenCRL, "client-crl-file", "", "proxy client certificate revocation list file.")
	cmd.Flags().UintVar(&selfSignedCertValidity, "self-signed-cert-validity", 1, "The validity period of the proxy certificates, unit is year")

	// experimental flags
	cmd.Flags().BoolVar(&grpcProxyEnableOrdering, "experimental-serializable-ordering", false, "Ensure serializable reads have monotonically increasing store revisions across endpoints.")
	cmd.Flags().StringVar(&grpcProxyLeasing, "experimental-leasing-prefix", "/onemeta", "leasing metadata prefix for disconnected linearized reads.")

	// debug 级别日志
	cmd.Flags().BoolVar(&grpcProxyDebug, "debug", false, "Enable debug-level logging for grpc-proxy.")

	// 每个 client 能打开的最大的逻辑连接数量，即并发请求数，默认无限制
	cmd.Flags().Uint32Var(&maxConcurrentStreams, "max-concurrent-streams", math.MaxUint32, "Maximum concurrent streams that each client can open at a time.")

	return &cmd
}

func startGRPCProxy(cmd *cobra.Command, args []string) {
	// 1、校验参数的合法性
	checkArgs()
	lvl := zap.InfoLevel
	if grpcProxyDebug {
		lvl = zap.DebugLevel
		grpc.EnableTracing = true
	}
	lg, err := logutil.CreateDefaultZapLogger(lvl)
	if err != nil {
		panic(err)
	}
	defer lg.Sync()

	grpclog.SetLoggerV2(zapgrpc.NewLogger(lg))

	// The proxy itself (ListenCert) can have not-empty CN.
	// The empty CN is required for grpcProxyCert.
	// Please see https://github.com/etcd-io/etcd/issues/11970#issuecomment-687875315  for more context.
	// 2、判断是否校验 https 证书
	tlsInfo := newTLS(grpcProxyListenCA, grpcProxyListenCert, grpcProxyListenKey, false)
	if len(grpcProxyListenCipherSuites) > 0 {
		cs, err := tlsutil.GetCipherSuites(grpcProxyListenCipherSuites)
		if err != nil {
			log.Fatal(err)
		}
		tlsInfo.CipherSuites = cs
	}
	if tlsInfo == nil && grpcProxyListenAutoTLS {
		host := []string{"https://" + grpcProxyListenAddr}
		dir := filepath.Join(grpcProxyDataDir, "fixtures", "proxy")
		autoTLS, err := transport.SelfCert(lg, dir, host, selfSignedCertValidity)
		if err != nil {
			log.Fatal(err)
		}
		tlsInfo = &autoTLS
	}

	if tlsInfo != nil {
		lg.Info("gRPC proxy server TLS", zap.String("tls-info", fmt.Sprintf("%+v", tlsInfo)))
	}

	// 3、生成流量分发的 cmux，用来将同一个端口的 HTTP 和 gRPC 协议的流量发到对应的服务处理（监听 23790 端口）
	m := mustListenCMux(lg, tlsInfo)
	// 4、创建 gRPC 服务的监听器
	grpcl := m.Match(cmux.HTTP2())
	defer func() {
		grpcl.Close()
		lg.Info("stop listening gRPC proxy client requests", zap.String("address", grpcProxyListenAddr))
	}()

	// 5、创建一个 client，和 etcd-server 建立连接
	client := mustNewClient(lg)

	// The proxy client is used for self-healthchecking.
	// TODO: The mechanism should be refactored to use internal connection.
	// 6、创建一个 client，和 etcd-proxy 自身建立连接
	var proxyClient *clientv3.Client
	if grpcProxyAdvertiseClientURL != "" {
		proxyClient = mustNewProxyClient(lg, tlsInfo)
	}

	// 7、 一些性能和资源监控的封装，如Prometheus，PProf等
	httpClient := mustNewHTTPClient(lg)

	// 8、设置 http 路由
	srvhttp, httpl := mustHTTPListener(lg, m, tlsInfo, client, proxyClient)

	if err := http2.ConfigureServer(srvhttp, &http2.Server{
		MaxConcurrentStreams: maxConcurrentStreams,
	}); err != nil {
		lg.Fatal("Failed to configure the http server", zap.Error(err))
	}

	errc := make(chan error, 3)
	// 9、启动 gRPC 服务
	go func() { errc <- newGRPCProxyServer(lg, client).Serve(grpcl) }()
	// 10、启动 http 服务
	go func() { errc <- srvhttp.Serve(httpl) }()
	// 11、启动 cmux 分发器
	go func() { errc <- m.Serve() }()
	if len(grpcProxyMetricsListenAddr) > 0 {
		mhttpl := mustMetricsListener(lg, tlsInfo)
		go func() {
			mux := http.NewServeMux()
			grpcproxy.HandleMetrics(mux, httpClient, client.Endpoints())
			grpcproxy.HandleHealth(lg, mux, client)
			grpcproxy.HandleProxyMetrics(mux)
			grpcproxy.HandleProxyHealth(lg, mux, proxyClient)
			lg.Info("gRPC proxy server metrics URL serving")
			herr := http.Serve(mhttpl, mux)
			if herr != nil {
				lg.Fatal("gRPC proxy server metrics URL returned", zap.Error(herr))
			} else {
				lg.Info("gRPC proxy server metrics URL returned")
			}
		}()
	}

	lg.Info("started gRPC proxy", zap.String("address", grpcProxyListenAddr))

	// grpc-proxy is initialized, ready to serve
	notifySystemd(lg)

	fmt.Fprintln(os.Stderr, <-errc)
	os.Exit(1)
}

func checkArgs() {
	if grpcProxyResolverPrefix != "" && grpcProxyResolverTTL < 1 {
		fmt.Fprintln(os.Stderr, fmt.Errorf("invalid resolver-ttl %d", grpcProxyResolverTTL))
		os.Exit(1)
	}
	if grpcProxyResolverPrefix == "" && grpcProxyResolverTTL > 0 {
		fmt.Fprintln(os.Stderr, fmt.Errorf("invalid resolver-prefix %q", grpcProxyResolverPrefix))
		os.Exit(1)
	}
	if grpcProxyResolverPrefix != "" && grpcProxyResolverTTL > 0 && grpcProxyAdvertiseClientURL == "" {
		fmt.Fprintln(os.Stderr, fmt.Errorf("invalid advertise-client-url %q", grpcProxyAdvertiseClientURL))
		os.Exit(1)
	}
	if grpcProxyListenAutoTLS && selfSignedCertValidity == 0 {
		fmt.Fprintln(os.Stderr, fmt.Errorf("selfSignedCertValidity is invalid,it should be greater than 0"))
		os.Exit(1)
	}
}

func mustNewClient(lg *zap.Logger) *clientv3.Client {
	// 优先从 DNS 服务发现 etcd-server 的地址
	srvs := discoverEndpoints(lg, grpcProxyDNSCluster, grpcProxyCA, grpcProxyInsecureDiscovery, grpcProxyDNSClusterServiceName)
	eps := srvs.Endpoints
	if len(eps) == 0 {
		// 如果未配置服务发现，则使用配置文件指定的 etcd-server 地址
		eps = grpcProxyEndpoints
	}
	cfg, err := newClientCfg(lg, eps)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg.DialOptions = append(cfg.DialOptions,
		grpc.WithUnaryInterceptor(grpcproxy.AuthUnaryClientInterceptor))
	cfg.DialOptions = append(cfg.DialOptions,
		grpc.WithStreamInterceptor(grpcproxy.AuthStreamClientInterceptor))
	cfg.Logger = lg.Named("client")
	// 创建一个 etcd client 对象，连接到 etcd-server
	client, err := clientv3.New(*cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return client
}

func mustNewProxyClient(lg *zap.Logger, tls *transport.TLSInfo) *clientv3.Client {
	eps := []string{grpcProxyAdvertiseClientURL}
	cfg, err := newProxyClientCfg(lg.Named("client"), eps, tls)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client, err := clientv3.New(*cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lg.Info("create proxy client", zap.String("grpcProxyAdvertiseClientURL", grpcProxyAdvertiseClientURL))
	return client
}

func newProxyClientCfg(lg *zap.Logger, eps []string, tls *transport.TLSInfo) (*clientv3.Config, error) {
	cfg := clientv3.Config{
		Endpoints:   eps,
		DialTimeout: 5 * time.Second,
		Logger:      lg,
	}
	if tls != nil {
		clientTLS, err := tls.ClientConfig()
		if err != nil {
			return nil, err
		}
		cfg.TLS = clientTLS
	}
	return &cfg, nil
}

func newClientCfg(lg *zap.Logger, eps []string) (*clientv3.Config, error) {
	// set tls if any one tls option set
	cfg := clientv3.Config{
		Endpoints:        eps,
		AutoSyncInterval: grpcProxyEndpointsAutoSyncInterval,
		DialTimeout:      5 * time.Second,
	}

	if grpcMaxCallSendMsgSize > 0 {
		cfg.MaxCallSendMsgSize = grpcMaxCallSendMsgSize
	}
	if grpcMaxCallRecvMsgSize > 0 {
		cfg.MaxCallRecvMsgSize = grpcMaxCallRecvMsgSize
	}

	tls := newTLS(grpcProxyCA, grpcProxyCert, grpcProxyKey, true)
	if tls == nil && grpcProxyInsecureSkipTLSVerify {
		tls = &transport.TLSInfo{}
	}
	if tls != nil {
		clientTLS, err := tls.ClientConfig()
		if err != nil {
			return nil, err
		}
		clientTLS.InsecureSkipVerify = grpcProxyInsecureSkipTLSVerify
		if clientTLS.InsecureSkipVerify {
			lg.Warn("--insecure-skip-tls-verify was given, this grpc proxy process skips authentication of etcd server TLS certificates. This option should be enabled only for testing purposes.")
		}
		cfg.TLS = clientTLS
		lg.Info("gRPC proxy client TLS", zap.String("tls-info", fmt.Sprintf("%+v", tls)))
	}
	return &cfg, nil
}

func newTLS(ca, cert, key string, requireEmptyCN bool) *transport.TLSInfo {
	if ca == "" && cert == "" && key == "" {
		return nil
	}
	return &transport.TLSInfo{TrustedCAFile: ca, CertFile: cert, KeyFile: key, EmptyCN: requireEmptyCN}
}

func mustListenCMux(lg *zap.Logger, tlsinfo *transport.TLSInfo) cmux.CMux {
	// 设置监听端口，默认为 23790
	l, err := net.Listen("tcp", grpcProxyListenAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if l, err = transport.NewKeepAliveListener(l, "tcp", nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if tlsinfo != nil {
		tlsinfo.CRLFile = grpcProxyListenCRL
		if l, err = transport.NewTLSListener(l, tlsinfo); err != nil {
			lg.Fatal("failed to create TLS listener", zap.Error(err))
		}
	}

	lg.Info("listening for gRPC proxy client requests", zap.String("address", grpcProxyListenAddr))
	// 返回流量分发 CMux
	return cmux.New(l)
}

func newGRPCProxyServer(lg *zap.Logger, client *clientv3.Client) *grpc.Server {
	if grpcProxyEnableOrdering {
		vf := ordering.NewOrderViolationSwitchEndpointClosure(client)
		client.KV = ordering.NewKV(client.KV, vf)
		lg.Info("waiting for linearized read from cluster to recover ordering")
		for {
			_, err := client.KV.Get(context.TODO(), "_", clientv3.WithKeysOnly())
			if err == nil {
				break
			}
			lg.Warn("ordering recovery failed, retrying in 1s", zap.Error(err))
			time.Sleep(time.Second)
		}
	}

	// 创建 namespace 相关的 KV、Watcher、Lease
	if len(grpcProxyNamespace) > 0 {
		client.KV = namespace.NewKV(client.KV, grpcProxyNamespace)
		client.Watcher = namespace.NewWatcher(client.Watcher, grpcProxyNamespace)
		client.Lease = namespace.NewLease(client.Lease, grpcProxyNamespace)
	}

	if len(grpcProxyLeasing) > 0 {
		client.KV, _, _ = leasing.NewKV(client, grpcProxyLeasing)
	}

	kvp, _ := grpcproxy.NewKvProxy(client)
	watchp, _ := grpcproxy.NewWatchProxy(client.Ctx(), lg, client)
	if grpcProxyResolverPrefix != "" {
		grpcproxy.Register(lg, client, grpcProxyResolverPrefix, grpcProxyAdvertiseClientURL, grpcProxyResolverTTL)
	}
	clusterp, _ := grpcproxy.NewClusterProxy(lg, client, grpcProxyAdvertiseClientURL, grpcProxyResolverPrefix)
	leasep, _ := grpcproxy.NewLeaseProxy(client.Ctx(), client)

	mainp := grpcproxy.NewMaintenanceProxy(client)
	authp := grpcproxy.NewAuthProxy(client)
	electionp := grpcproxy.NewElectionProxy(client)
	lockp := grpcproxy.NewLockProxy(client)

	gopts := []grpc.ServerOption{
		grpc.StreamInterceptor(grpc_prometheus.StreamServerInterceptor),
		grpc.UnaryInterceptor(grpc_prometheus.UnaryServerInterceptor),
		grpc.MaxConcurrentStreams(math.MaxUint32),
	}
	if grpcKeepAliveMinTime > time.Duration(0) {
		gopts = append(gopts, grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             grpcKeepAliveMinTime,
			PermitWithoutStream: false,
		}))
	}
	if grpcKeepAliveInterval > time.Duration(0) ||
		grpcKeepAliveTimeout > time.Duration(0) {
		gopts = append(gopts, grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    grpcKeepAliveInterval,
			Timeout: grpcKeepAliveTimeout,
		}))
	}

	server := grpc.NewServer(gopts...)

	pb.RegisterKVServer(server, kvp)
	pb.RegisterWatchServer(server, watchp)
	pb.RegisterClusterServer(server, clusterp)
	pb.RegisterLeaseServer(server, leasep)
	pb.RegisterMaintenanceServer(server, mainp)
	pb.RegisterAuthServer(server, authp)
	v3electionpb.RegisterElectionServer(server, electionp)
	v3lockpb.RegisterLockServer(server, lockp)

	return server
}

func mustHTTPListener(lg *zap.Logger, m cmux.CMux, tlsinfo *transport.TLSInfo, c *clientv3.Client, proxy *clientv3.Client) (*http.Server, net.Listener) {
	httpClient := mustNewHTTPClient(lg)
	httpmux := http.NewServeMux()
	httpmux.HandleFunc("/", http.NotFound) // 配置 http 路由
	// 测试效果
	httpmux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("Hello, world!")) })
	grpcproxy.HandleMetrics(httpmux, httpClient, c.Endpoints())
	grpcproxy.HandleHealth(lg, httpmux, c)
	grpcproxy.HandleProxyMetrics(httpmux)
	grpcproxy.HandleProxyHealth(lg, httpmux, proxy)
	if grpcProxyEnablePprof {
		for p, h := range debugutil.PProfHandlers() {
			httpmux.Handle(p, h)
		}
		lg.Info("gRPC proxy enabled pprof", zap.String("path", debugutil.HTTPPrefixPProf))
	}
	srvhttp := &http.Server{
		Handler:  httpmux,
		ErrorLog: log.New(ioutil.Discard, "net/http", 0),
	}

	if tlsinfo == nil {
		return srvhttp, m.Match(cmux.HTTP1())
	}

	srvTLS, err := tlsinfo.ServerConfig()
	if err != nil {
		lg.Fatal("failed to set up TLS", zap.Error(err))
	}
	srvhttp.TLSConfig = srvTLS
	return srvhttp, m.Match(cmux.Any())
}

func mustNewHTTPClient(lg *zap.Logger) *http.Client {
	transport, err := newHTTPTransport(grpcProxyCA, grpcProxyCert, grpcProxyKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return &http.Client{Transport: transport}
}

func newHTTPTransport(ca, cert, key string) (*http.Transport, error) {
	tr := &http.Transport{}

	if ca != "" && cert != "" && key != "" {
		caCert, err := ioutil.ReadFile(ca)
		if err != nil {
			return nil, err
		}
		keyPair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return nil, err
		}
		caPool := x509.NewCertPool()
		caPool.AppendCertsFromPEM(caCert)

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{keyPair},
			RootCAs:      caPool,
		}
		tlsConfig.BuildNameToCertificate()
		tr.TLSClientConfig = tlsConfig
	} else if grpcProxyInsecureSkipTLSVerify {
		tlsConfig := &tls.Config{InsecureSkipVerify: grpcProxyInsecureSkipTLSVerify}
		tr.TLSClientConfig = tlsConfig
	}
	return tr, nil
}

func mustMetricsListener(lg *zap.Logger, tlsinfo *transport.TLSInfo) net.Listener {
	murl, err := url.Parse(grpcProxyMetricsListenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot parse %q", grpcProxyMetricsListenAddr)
		os.Exit(1)
	}
	ml, err := transport.NewListener(murl.Host, murl.Scheme, tlsinfo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lg.Info("gRPC proxy listening for metrics", zap.String("address", murl.String()))
	return ml
}
