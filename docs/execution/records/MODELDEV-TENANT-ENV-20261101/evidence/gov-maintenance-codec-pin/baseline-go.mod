module go-wind-admin

go 1.26.10

require (
	entgo.io/ent v0.14.6
	github.com/alicebob/miniredis/v2 v2.39.0
	github.com/coder/websocket v1.8.15
	github.com/envoyproxy/protoc-gen-validate v1.3.3
	github.com/getkin/kin-openapi v0.149.0
	github.com/go-kratos/kratos/v2 v2.9.2
	github.com/go-sql-driver/mysql v1.10.1
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/gnostic v0.7.1
	github.com/hibiken/asynq v0.26.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/jinzhu/copier v0.4.0
	github.com/jinzhu/inflection v1.0.0
	github.com/liangzai006/ani-inference-service/api v0.1.2-image-registry.2.0.20261029033104-a8b505b9e2ea
	github.com/liangzai006/ani-model-service/api v0.0.0-20261028165608-620bcb1503a6
	github.com/lib/pq v1.12.3
	github.com/mileusna/useragent v1.3.5
	github.com/pquerna/otp v1.5.0
	github.com/redis/go-redis/v9 v9.22.0
	github.com/stretchr/testify v1.12.1
	github.com/xiaoqidun/entps v1.50.1
	github.com/zhangzhe-ctrl/ani-accelerator-service v0.0.0-20261027163656-86ce81af852a
	github.com/zhangzhe-ctrl/ani-modeldev-service v0.0.0-20261029062749-c99c9f1081c7
	github.com/zhangzhe-ctrl/ani-observability-service/api v0.1.3-0.20261028072444-5377d534eac6
	github.com/zhangzhe-ctrl/ani-resource-service v0.0.0-20261030162442-d09b4230ab65
	github.com/zhangzhe-ctrl/ani-resource-service/api/instance/terminal v0.0.0-20261013075021-0937f5aa9c6c
	github.com/zhangzhe-ctrl/ani-resource-service/api/vm/console v0.0.0-20261023185414-0a53b99c8d9f
	github.com/zhangzhe-ctrl/ani-session-gateway/api v0.1.1-0.20261024051540-811cc3839244
	go.opentelemetry.io/otel/trace v1.46.0
	golang.org/x/net v0.60.0
	google.golang.org/genproto v0.0.0-20260908043556-f8649ddbbfe6
	google.golang.org/genproto/googleapis/api v0.0.0-20260908043556-f8649ddbbfe6
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
	k8s.io/api v0.36.3
	k8s.io/apimachinery v0.36.3
	k8s.io/client-go v0.36.3
	modernc.org/sqlite v1.58.0
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dlclark/regexp2 v1.12.0 // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.0 // indirect
	github.com/go-openapi/jsonreference v1.0.0 // indirect
	github.com/go-openapi/swag v0.28.0 // indirect
	github.com/go-openapi/swag/cmdutils v0.28.0 // indirect
	github.com/go-openapi/swag/conv v0.28.0 // indirect
	github.com/go-openapi/swag/fileutils v0.28.0 // indirect
	github.com/go-openapi/swag/jsonutils v0.28.0 // indirect
	github.com/go-openapi/swag/loading v0.28.0 // indirect
	github.com/go-openapi/swag/mangling v0.28.0 // indirect
	github.com/go-openapi/swag/netutils v0.28.0 // indirect
	github.com/go-openapi/swag/pools v0.28.0 // indirect
	github.com/go-openapi/swag/stringutils v0.28.0 // indirect
	github.com/go-openapi/swag/typeutils v0.28.0 // indirect
	github.com/go-openapi/swag/yamlutils v0.28.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/yuin/gopher-lua v1.1.2 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/term v0.46.0 // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260317180543-43fb72c5454a // indirect
	k8s.io/utils v0.0.0-20260210185600-b8788abfbbc2 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

require (
	ariga.io/atlas v1.3.0 // indirect
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260825204119-511051f7f437.2 // indirect
	buf.build/go/protovalidate v1.4.0
	cel.dev/cel-go v0.32.0 // indirect
	cel.dev/expr v0.25.3 // indirect
	dario.cat/mergo v1.0.2 // indirect
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/XSAM/otelsql v0.44.0
	github.com/agext/levenshtein v1.2.3 // indirect
	github.com/andybalholm/brotli v1.2.3 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/apparentlymart/go-textseg/v17 v17.0.1 // indirect
	github.com/bmatcuk/doublestar v1.3.4 // indirect
	github.com/bmatcuk/doublestar/v4 v4.10.0 // indirect
	github.com/boombuler/barcode v1.0.1-0.20190219062509-6c824513bacc // indirect
	github.com/bwmarrin/snowflake v0.3.0
	github.com/casbin/casbin/v2 v2.135.0
	github.com/casbin/govaluate v1.10.0 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1
	github.com/go-kratos/aegis v0.2.0
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/go-openapi/inflect v1.0.0 // indirect
	github.com/go-openapi/jsonpointer v1.0.0 // indirect
	github.com/go-playground/form/v4 v4.3.1 // indirect
	github.com/go-test/deep v1.0.8 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/golang/freetype v0.0.0-20170609003504-e2365dfdc4a0 // indirect
	github.com/google/gnostic-models v0.7.1 // indirect
	github.com/google/go-cmp v0.7.0
	github.com/google/subcommands v1.2.0
	github.com/google/uuid v1.6.0
	github.com/gorilla/handlers v1.5.2
	github.com/gorilla/mux v1.8.1
	github.com/graph-gophers/dataloader/v7 v7.1.3
	github.com/grpc-ecosystem/go-grpc-middleware v1.4.0
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.30.0 // indirect
	github.com/hashicorp/hcl/v2 v2.24.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/lithammer/shortuuid/v4 v4.3.0
	github.com/lufia/plan9stats v0.0.0-20260802145828-341c2f0c90b5 // indirect
	github.com/lyft/protoc-gen-star/v2 v2.0.4
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-runewidth v0.0.28 // indirect
	github.com/mattn/go-sqlite3 v1.14.52 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/mojocn/base64Captcha v1.3.8
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/oasdiff/yaml v0.1.1 // indirect
	github.com/oasdiff/yaml3 v0.0.14 // indirect
	github.com/olekukonko/cat v0.0.0-20250911104152-50322a0618f6 // indirect
	github.com/olekukonko/errors v1.3.0 // indirect
	github.com/olekukonko/ll v0.1.8 // indirect
	github.com/olekukonko/tablewriter v1.1.4
	github.com/openzipkin/zipkin-go v0.4.3 // indirect
	github.com/power-devops/perfstat v0.0.0-20260805114148-88456608a4f6 // indirect
	github.com/redis/go-redis/extra/rediscmd/v9 v9.22.0 // indirect
	github.com/redis/go-redis/extra/redisotel/v9 v9.22.0
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/rs/xid v1.6.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/segmentio/ksuid v1.0.4
	github.com/shirou/gopsutil/v3 v3.24.5 // indirect
	github.com/shoenig/go-m1cpu v0.2.2 // indirect
	github.com/sony/sonyflake v1.3.0
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/swaggest/swgui v1.8.5
	github.com/tjfoc/gmsm v1.4.1
	github.com/tklauser/go-sysconf v0.4.0 // indirect
	github.com/tklauser/numcpus v0.12.0 // indirect
	github.com/vearutop/statigz v1.5.0
	github.com/wenlng/go-captcha/v2 v2.0.5
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	github.com/zclconf/go-cty v1.19.0 // indirect
	github.com/zclconf/go-cty-yaml v1.2.0 // indirect
	github.com/zhangzhe-ctrl/ani-governance/api/quota v0.0.0-20261017184327-1c605c793b88
	go.einride.tech/aip v0.86.3
	go.mongodb.org/mongo-driver/v2 v2.9.0
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.46.0
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.46.0
	go.opentelemetry.io/otel/exporters/zipkin v1.45.0
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/sdk v1.46.0
	go.opentelemetry.io/proto/otlp v1.11.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.57.0
	golang.org/x/exp v0.0.0-20260824195058-e88cd73687aa
	golang.org/x/image v0.40.0 // indirect
	golang.org/x/mod v0.41.0
	golang.org/x/sync v0.23.0
	golang.org/x/sys v0.48.0
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.org/x/tools v0.49.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260908043556-f8649ddbbfe6
	gopkg.in/cenkalti/backoff.v1 v1.1.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
