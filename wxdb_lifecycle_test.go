package main

import (
	"errors"
	"testing"
	"time"
)

func TestWxDBCloseWaitsForQueuedQueryAndRejectsLaterQueries(t *testing.T) {
	d := &WxDB{reqCh: make(chan dbReq, 8), done: make(chan struct{})}
	started := make(chan struct{})
	release := make(chan struct{})
	closeSeen := make(chan struct{})
	go func() {
		defer close(d.done)
		d.serveRequests(func(op string, _ map[string]any) (any, error) {
			if op == "query" {
				close(started)
				<-release
				return "done", nil
			}
			if op == "__close__" {
				close(closeSeen)
			}
			return nil, nil
		})
	}()

	queryResult := make(chan error, 1)
	go func() {
		value, err := d.query("query", nil)
		if err == nil && value != "done" {
			err = errors.New("unexpected query result")
		}
		queryResult <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("query did not start")
	}

	closeDone := make(chan struct{})
	go func() {
		d.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("Close returned while a submitted query was still running")
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-queryResult:
		if err != nil {
			t.Fatalf("in-flight query failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight query did not complete")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after the query drained")
	}
	select {
	case <-closeSeen:
	default:
		t.Fatal("worker did not process the close request")
	}
	if _, err := d.query("query", nil); err == nil {
		t.Fatal("query after Close should fail")
	}
	d.Close()
}
