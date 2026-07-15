package main

import (
	"context"
	"errors"
	"testing"
)

func TestReportStatusServeErrorCancelsAndRecords(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	want := errors.New("status listener failed")

	reportStatusServeError(want, cancel, errCh)

	select {
	case <-ctx.Done():
	default:
		t.Fatal("status server failure did not request graceful shutdown")
	}
	select {
	case got := <-errCh:
		if !errors.Is(got, want) {
			t.Fatalf("recorded error = %v, want %v", got, want)
		}
	default:
		t.Fatal("status server failure was not recorded for the process exit code")
	}
}

func TestReportStatusServeErrorDoesNotBlockWhenAlreadyRecorded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	first := errors.New("first status listener failed")
	errCh <- first

	reportStatusServeError(errors.New("second status listener failed"), cancel, errCh)

	select {
	case <-ctx.Done():
	default:
		t.Fatal("later status server failure did not request graceful shutdown")
	}
	if got := <-errCh; !errors.Is(got, first) {
		t.Fatalf("recorded error = %v, want first error %v", got, first)
	}
}
