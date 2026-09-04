package services

import (
	"sync"
	"time"
)

// ConnectionCoordinator serializa operações de connect/geração de QR por
// instância e invalida ciclos de QR ainda pendentes quando o estado de conexão
// muda (Connected/PairSuccess, logout, disconnect...).
//
// O lock por instância precisa ser COMPARTILHADO entre o BridgeService (que
// executa o auto-reconnect do QRTimeout) e o AdminService (que executa o
// /connect e o /recreate do painel) — se cada serviço tivesse o próprio lock,
// as chamadas de ConnectInstance para a mesma instância continuariam
// concorrendo.
type ConnectionCoordinator struct {
	lockMu sync.Mutex
	locks  map[string]*sync.Mutex
	epoch  sync.Map // instanceName -> int64 (UnixNano da última mudança de estado)
}

func NewConnectionCoordinator() *ConnectionCoordinator {
	return &ConnectionCoordinator{locks: make(map[string]*sync.Mutex)}
}

// LockInstance devolve uma função de unlock para o lock exclusivo da instância.
func (c *ConnectionCoordinator) LockInstance(instanceName string) func() {
	c.lockMu.Lock()
	mu, ok := c.locks[instanceName]
	if !ok {
		mu = &sync.Mutex{}
		c.locks[instanceName] = mu
	}
	c.lockMu.Unlock()

	mu.Lock()
	return mu.Unlock
}

// QREpoch devolve a "era" atual da instância. Um auto-reconnect que capturou a
// era antes de dormir e, ao acordar, encontra a era diferente, sabe que o estado
// mudou (ex.: Connected/PairSuccess chegou no meio do caminho) e deve abortar.
func (c *ConnectionCoordinator) QREpoch(instanceName string) int64 {
	if v, ok := c.epoch.Load(instanceName); ok {
		return v.(int64)
	}
	return 0
}

// BumpQREpoch invalida ciclos de QR pendentes para a instância: a partir daqui,
// qualquer auto-reconnect aguardando com uma era antiga aborta ao comparar a era.
func (c *ConnectionCoordinator) BumpQREpoch(instanceName string) {
	c.epoch.Store(instanceName, time.Now().UnixNano())
}

// Coordenador único do processo — compartilhado entre os serviços.
var instanceCoordinator = NewConnectionCoordinator()

func lockInstanceConnect(instanceName string) func() {
	return instanceCoordinator.LockInstance(instanceName)
}

func bumpInstanceQREpoch(instanceName string) {
	instanceCoordinator.BumpQREpoch(instanceName)
}

func instanceQREpoch(instanceName string) int64 {
	return instanceCoordinator.QREpoch(instanceName)
}
