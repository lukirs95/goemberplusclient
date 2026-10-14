package emberplus_test

import (
	"context"
	"fmt"
	"log"
	"time"

	emberplus "github.com/lukirs95/goemberplusclient"
	"github.com/lukirs95/goemberplusclient/pkg/glow"
)

// The examples need a provider and are therefore not run by go test.

func Example() {
	ctx := context.Background()
	c, err := emberplus.Dial(ctx, "192.168.1.10:9000")
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	el, err := c.Get(ctx, "root/settings/info/version")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(el.(*glow.Parameter).Value)
}

// This example prints the provider tree, loading every directory.
func ExampleClient_Children() {
	ctx := context.Background()
	c, err := emberplus.Dial(ctx, "192.168.1.10:9000")
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	var walk func(path string, depth int)
	walk = func(path string, depth int) {
		children, err := c.Children(ctx, path)
		if err != nil {
			log.Fatal(err)
		}
		for _, child := range children {
			id, _ := c.IdentifierPath(ctx, child.ElementPath().String())
			fmt.Printf("%*s%s\n", 2*depth, "", id)
			if n, ok := child.(*glow.Node); ok && (n.IsOnline == nil || *n.IsOnline) {
				walk(child.ElementPath().String(), depth+1)
			}
		}
	}
	walk("", 0)
}

func ExampleClient_SetValue() {
	ctx := context.Background()
	c, err := emberplus.Dial(ctx, "192.168.1.10:9000")
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	v, err := c.SetValue(ctx, "root/dsp/gain", glow.Real(-3))
	if err != nil {
		log.Fatal(err)
	}
	if v != glow.Real(-3) {
		fmt.Println("the provider kept", v)
	}
}

func ExampleClient_Watch() {
	ctx := context.Background()
	c, err := emberplus.Dial(ctx, "192.168.1.10:9000")
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	stop, err := c.Watch(ctx, "root/dsp/gain", func(e emberplus.Event) {
		if v := e.Update.(*glow.Parameter).Value; v != nil {
			fmt.Println("gain changed to", v)
		}
	})
	if err != nil {
		log.Fatal(err)
	}
	defer stop()

	<-c.Done()
}

// Stream parameters such as meters only report values while subscribed.
// This example checks that a parameter is a stream before subscribing.
func ExampleClient_Subscribe() {
	ctx := context.Background()
	c, err := emberplus.Dial(ctx, "192.168.1.10:9000")
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	const meter = "root/mixer/ch1/level"
	el, err := c.Get(ctx, meter)
	if err != nil {
		log.Fatal(err)
	}
	if !el.(*glow.Parameter).IsStream() {
		log.Fatal(meter, " is not a stream")
	}

	unsubscribe, err := c.Subscribe(ctx, meter)
	if err != nil {
		log.Fatal(err)
	}
	defer unsubscribe()
	stop, err := c.Watch(ctx, meter, func(e emberplus.Event) {
		fmt.Println(e.Update.(*glow.Parameter).Value)
	})
	if err != nil {
		log.Fatal(err)
	}
	defer stop()

	time.Sleep(10 * time.Second)
}

func ExampleClient_Connect() {
	ctx := context.Background()
	c, err := emberplus.Dial(ctx, "192.168.1.10:9000")
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	// Connect source 3 to target 0, replacing its current sources.
	conn, err := c.Connect(ctx, "root/router/matrix", 0, []uint32{3}, glow.OpAbsolute)
	if err != nil {
		log.Fatal(err)
	}
	if conn.Disposition == glow.Locked {
		fmt.Println("target 0 is locked")
	}
}

func ExampleClient_Invoke() {
	ctx := context.Background()
	c, err := emberplus.Dial(ctx, "192.168.1.10:9000")
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	res, err := c.Invoke(ctx, "root/presets/recall", glow.Int(4))
	if err != nil {
		log.Fatal(err)
	}
	if !res.Success {
		fmt.Println("recall failed")
	}
}
