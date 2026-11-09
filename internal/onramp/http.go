package onramp

import (
 "encoding/json"
 "errors"
 "io"
 "net"
 "net/http"
 "net/netip"
 "os"
 "strings"
 "sync"
 "time"

 "github.com/nemo-crypto/pay/internal/auth"
)
func respond(w http.ResponseWriter,status int,v any){w.Header().Set("Content-Type","application/json");w.WriteHeader(status);_ = json.NewEncoder(w).Encode(v)}
func fail(w http.ResponseWriter,err error){status:=400;code:="REQUEST_FAILED";switch {case errors.Is(err,ErrNotFound):status=404;code="NOT_FOUND";case errors.Is(err,ErrConflict):status=409;code="CONFLICT";case errors.Is(err,ErrPending):status=409;code="WAITING_PROVIDER_REFERENCE"};respond(w,status,map[string]string{"code":code,"message":"操作未完成，请核对请求、订单状态或网关配置；不要重复创建购买意图"})}
func read(w http.ResponseWriter,r *http.Request,v any)error{r.Body=http.MaxBytesReader(w,r.Body,65536);d:=json.NewDecoder(r.Body);d.DisallowUnknownFields();if err:=d.Decode(v);err!=nil{return err};if err:=d.Decode(new(any));err!=io.EOF{return ErrConflict};return nil}
func clientIP(r *http.Request)string{
 host,_,_:=net.SplitHostPort(r.RemoteAddr);peer,err:=netip.ParseAddr(host);if err!=nil{return ""}
 // 仅信任指定反向代理写入的单一 X-Real-IP；代理必须覆盖外部传入值。
 for _,raw:=range strings.Split(os.Getenv("ONRAMP_TRUSTED_PROXY_CIDRS"),","){p,err:=netip.ParsePrefix(strings.TrimSpace(raw));if err==nil&&p.Contains(peer){if ip,err:=netip.ParseAddr(r.Header.Get("X-Real-IP"));err==nil{return ip.String()}}}
 return peer.String()
}
func (e *Engine) Handler(a auth.Auth,static http.Handler)http.Handler{
 m:=http.NewServeMux();var mu sync.Mutex;type bucket struct{At time.Time;N int};limits:=map[string]bucket{}
 secure:=func(fn func(http.ResponseWriter,*http.Request,string))http.HandlerFunc{return func(w http.ResponseWriter,r *http.Request){
  c,err:=a.Parse(r.Header.Get("Authorization"));if err!=nil{respond(w,401,map[string]string{"code":"UNAUTHORIZED"});return};if c.Guest{respond(w,403,map[string]string{"code":"GUEST_NOT_ELIGIBLE"});return}
  mu.Lock();now:=time.Now();if len(limits)>1000{for k,v:=range limits{if now.Sub(v.At)>time.Minute{delete(limits,k)}}};b:=limits[c.Subject];if now.Sub(b.At)>time.Minute{b=bucket{At:now}};b.N++;limits[c.Subject]=b;blocked:=b.N>120||len(limits)>10000;mu.Unlock();if blocked{w.Header().Set("Retry-After","60");respond(w,429,map[string]string{"code":"RATE_LIMIT"});return};fn(w,r,c.Subject)
 }}
 m.HandleFunc("GET /onramp/providers",func(w http.ResponseWriter,r *http.Request){
  catalog:=[]map[string]any{};for _,name:=range Names{a:=e.Adapters[name];if a==nil{continue};catalog=append(catalog,map[string]any{"id":name,"sandbox":a.Sandbox,"guide":"/guides/"+name})}
  respond(w,200,map[string]any{"mode":e.Mode,"providers":catalog,"fiat_currency":"USD","min_minor":3000,"max_minor":100000,"asset":"USDC","network":"ethereum","quote_type":map[bool]string{true:"mock_estimate",false:"provider_checkout_final"}[e.Mode=="demo"]})
 })
 m.HandleFunc("POST /onramp/orders",secure(func(w http.ResponseWriter,r *http.Request,user string){var in Input;if err:=read(w,r,&in);err!=nil{fail(w,err);return};o,err:=e.Create(r.Context(),user,r.Header.Get("Idempotency-Key"),clientIP(r),in);if err!=nil{fail(w,err);return};respond(w,201,o)}))
 m.HandleFunc("GET /onramp/orders",secure(func(w http.ResponseWriter,r *http.Request,user string){v,err:=e.List(r.Context(),user);if err!=nil{fail(w,err);return};respond(w,200,map[string]any{"orders":v})}))
 m.HandleFunc("GET /onramp/orders/{id}",secure(func(w http.ResponseWriter,r *http.Request,user string){o,err:=e.Get(r.Context(),user,r.PathValue("id"));if err!=nil{fail(w,err);return};respond(w,200,o)}))
 m.HandleFunc("POST /onramp/orders/{id}/sync",secure(func(w http.ResponseWriter,r *http.Request,user string){o,err:=e.Sync(r.Context(),user,r.PathValue("id"));if err!=nil{fail(w,err);return};respond(w,200,o)}))
 m.HandleFunc("POST /onramp/orders/{id}/bind",secure(func(w http.ResponseWriter,r *http.Request,user string){var in struct{Ref string `json:"provider_order_id"`;View string `json:"purchase_view_token"`};if err:=read(w,r,&in);err!=nil{fail(w,err);return};o,err:=e.Bind(r.Context(),user,r.PathValue("id"),in.Ref,in.View);if err!=nil{fail(w,err);return};respond(w,200,o)}))
 m.HandleFunc("POST /onramp/webhooks/{provider}",func(w http.ResponseWriter,r *http.Request){r.Body=http.MaxBytesReader(w,r.Body,1<<20);b,err:=io.ReadAll(r.Body);if err!=nil{respond(w,413,map[string]string{"code":"PAYLOAD_TOO_LARGE"});return};if err=e.Ingest(r.Context(),r,b,r.PathValue("provider"));err!=nil{fail(w,err);return};respond(w,200,map[string]bool{"received":true})})
 if e.Mode=="demo"{m.HandleFunc("POST /onramp/demo/{id}/advance",secure(func(w http.ResponseWriter,r *http.Request,user string){var in struct{Action string `json:"action"`;Deliver *bool `json:"deliver_webhook"`};if err:=read(w,r,&in);err!=nil{fail(w,err);return};o,err:=e.Advance(r.Context(),user,r.PathValue("id"),in.Action,in.Deliver==nil||*in.Deliver);if err!=nil{fail(w,err);return};respond(w,200,o)}))}
 m.Handle("/",static);return m
}
