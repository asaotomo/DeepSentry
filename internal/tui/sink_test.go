package tui

import (
	"testing"
	"time"

	"ai-edr/internal/harness"
)

func TestChannelSinkNeverDropsSemanticEventAndCloseUnblocks(t *testing.T) {
	sink := NewChannelSink(1)
	sink.Emit(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "fills buffer"})
	delivered := make(chan struct{})
	go func() {
		sink.Emit(harness.UIEvent{Kind: harness.EventFinish, Message: "final report"})
		close(delivered)
	}()

	select {
	case <-delivered:
		t.Fatal("semantic event unexpectedly returned while queue was full")
	case <-time.After(30 * time.Millisecond):
	}
	<-sink.Events()
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("semantic event was not delivered after queue drained")
	}
	if event := <-sink.Events(); event.Kind != harness.EventFinish {
		t.Fatalf("got %s want finish", event.Kind)
	}

	sink.Emit(harness.UIEvent{Kind: harness.EventInfo, Message: "fills again"})
	blocked := make(chan struct{})
	go func() {
		sink.Emit(harness.UIEvent{Kind: harness.EventError, Message: "blocked"})
		close(blocked)
	}()
	time.Sleep(20 * time.Millisecond)
	sink.Close()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock semantic emitter")
	}
}

func TestChannelSinkDropsPreviewChunksWithoutBlockingAndDeliversFullEnd(t *testing.T) {
	sink := NewChannelSink(1)
	sink.Emit(harness.UIEvent{Kind: harness.EventStreamDelta, Message: "first"})
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			sink.Emit(harness.UIEvent{Kind: harness.EventStreamDelta, Message: "later"})
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("full display queue stalled the model stream")
	}
	if event := <-sink.Events(); event.Kind != harness.EventStreamDelta || event.Message != "first" {
		t.Fatalf("first preview chunk lost: %#v", event)
	}
	delivered := make(chan struct{})
	go func() {
		sink.Emit(harness.UIEvent{Kind: harness.EventStreamEnd, Detail: "first later"})
		close(delivered)
	}()
	notice := <-sink.Events()
	if notice.Kind != harness.EventInfo || notice.Message == "" {
		t.Fatalf("missing dropped-preview notice: %#v", notice)
	}
	end := <-sink.Events()
	if end.Kind != harness.EventStreamEnd || end.Detail != "first later" {
		t.Fatalf("full stream end lost: %#v", end)
	}
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("stream end did not finish delivery")
	}
}
