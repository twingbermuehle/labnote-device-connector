package main

import (
	"context"
	"fmt"

	"github.com/gopcua/opcua"
	"github.com/labnote/labnote-device-connector/internal/lads"
	"github.com/labnote/labnote-device-connector/internal/mapping"
	"github.com/labnote/labnote-device-connector/internal/model"
	"github.com/labnote/labnote-device-connector/internal/profiles"
)

func main() {
	ctx := context.Background()
	c, _ := opcua.NewClient("opc.tcp://127.0.0.1:26543")
	if err := c.Connect(ctx); err != nil {
		panic(err)
	}
	defer c.Close(ctx)
	b := lads.NewBrowser(c)
	set, _ := profiles.Load("")
	prof := set.Get("generic-lads")
	devs, _ := b.Devices(ctx)
	for _, d := range devs {
		sets, _ := b.ResultSetNodes(ctx, d.NodeID)
		for _, rs := range sets {
			results, _ := b.Results(ctx, rs)
			for _, r := range results {
				ts, ok := b.StoppedTime(ctx, r)
				fmt.Println("result", r, "stopped:", ts, ok)
				if !ok {
					continue
				}
				rec, err := mapping.Build(ctx, b, model.Instrument{ExternalDeviceID: "X", LADSNodeID: d.NodeID}, prof, r)
				fmt.Printf("  sample=%q method=%q operator=%q points=%d unitY=%q summaryKeys=%d err=%v\n",
					rec.SampleCode, rec.Method, rec.Operator, len(rec.Points), rec.UnitY, len(rec.Summary), err)
			}
			fmt.Println("watch nodes:", b.ChangeWatchNodes(ctx, rs))
		}
	}
}
