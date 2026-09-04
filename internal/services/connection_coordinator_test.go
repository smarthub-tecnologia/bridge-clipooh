package services

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLockInstanceConnectSerializes garante que duas operações de connect para a
// mesma instância não executam simultaneamente (o segundo caller bloqueia até o
// primeiro liberar o lock).
func TestLockInstanceConnectSerializes(t *testing.T) {
	c := NewConnectionCoordinator()

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredCount int32
	var wg sync.WaitGroup

	// Goroutine A: adquire o lock e segura até o `release`.
	wg.Add(1)
	go func() {
		defer wg.Done()
		unlock := c.LockInstance("inst-x")
		defer unlock()
		atomic.AddInt32(&enteredCount, 1)
		close(entered)
		<-release
	}()

	<-entered // A entrou e segura o lock

	// Goroutine B: tenta adquirir o lock da mesma instância em paralelo.
	wg.Add(1)
	go func() {
		defer wg.Done()
		unlock := c.LockInstance("inst-x")
		defer unlock()
		atomic.AddInt32(&enteredCount, 10)
	}()

	// Dá tempo de B tentar; como A ainda segura o lock, B não pode ter entrado.
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&enteredCount); got != 1 {
		t.Fatalf("lock da mesma instância não serializou: contador=%d (esperado 1 antes do release)", got)
	}

	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&enteredCount); got != 11 {
		t.Fatalf("após o release ambas as operações deveriam executar em sequência: contador=%d", got)
	}
}

// TestQREpochBump garante que BumpQREpoch invalida a era capturada anteriormente.
func TestQREpochBump(t *testing.T) {
	c := NewConnectionCoordinator()

	before := c.QREpoch("inst-y")
	c.BumpQREpoch("inst-y")
	after := c.QREpoch("inst-y")

	if before >= after {
		t.Fatalf("esperava era nova maior que a anterior (before=%d after=%d)", before, after)
	}
}

// TestLockInstanceIsPerInstance garante que instâncias diferentes não se
// bloqueiam mutuamente.
func TestLockInstanceIsPerInstance(t *testing.T) {
	c := NewConnectionCoordinator()

	unlockA := c.LockInstance("inst-a")
	defer unlockA()

	done := make(chan struct{})
	go func() {
		unlockB := c.LockInstance("inst-b")
		unlockB()
		close(done)
	}()

	select {
	case <-done:
		// ok: inst-b não esperou pelo lock de inst-a
	case <-time.After(500 * time.Millisecond):
		t.Fatal("lock deveria ser por instância — inst-b ficou bloqueada por inst-a")
	}
}
