package main
import (
 "encoding/json"
 "fmt"
 "github.com/SuperMarioYL/cachepin/internal/openai"
 "github.com/SuperMarioYL/cachepin/internal/session"
 "github.com/SuperMarioYL/cachepin/internal/pin"
)
func main(){
 var before,after []openai.Message
 json.Unmarshal([]byte(`[{"role":"system","content":"Be precise."},{"role":"user","content":"Review code."},{"role":"assistant","content":"Original answer."}]`),&before)
 json.Unmarshal([]byte(`[{"role":"system","content":"Be precise."},{"role":"user","content":"Review code."},{"role":"assistant","content":"Corrected answer."},{"role":"user","content":"Continue."}]`),&after)
 out,changed:=pin.Reconcile(before,after)
 fmt.Printf("common prefix: %d messages\nrewritten: %v\nmessage count: %d -> %d\n",session.LongestCommonPrefix(before,after),changed,len(after),len(out))
 for i,m:=range out{fmt.Printf("%d %s %s\n",i,m.Role,m.Content)}
}
