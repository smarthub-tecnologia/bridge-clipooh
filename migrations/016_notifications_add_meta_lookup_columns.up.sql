-- Substitui a antiga dependencia de wa_message_logs (tabela da plataforma,
-- ~40 colunas de billing/credito que este bridge nao usa) pra correlacionar
-- o callback de entrega da Meta com o evento original.
--
-- notifications.id ja FAZ esse papel de event_id: NotificationRepository.Create
-- usa o EventID informado pelo caller como id da linha quando ha exatamente 1
-- destinatario (unico caso em que o event_id identifica uma mensagem sem
-- ambiguidade). So falta saber POR QUAL instancia a mensagem foi enviada —
-- e isso essa migration adiciona.
ALTER TABLE notifications
    ADD COLUMN IF NOT EXISTS instance_name VARCHAR(255);

CREATE INDEX IF NOT EXISTS idx_notifications_instance_to
    ON notifications (instance_name, to_number, status);
