-- Vínculo obrigatório instância Evolution -> inbox Chatwoot.
-- Cada linha WhatsApp (instância) precisa de uma inbox Chatwoot própria para
-- onde as mensagens inbound devem ser entregues. Sem vínculo, o bridge NÃO
-- entrega a mensagem (evita cair em inbox errada quando a conta tem várias).
--
-- Os dados são preenchidos pelo dashboard (form de criação/edição de instância)
-- com o que o Chatwoot devolve ao criar uma inbox do tipo API:
--   - chatwoot_inbox_id: id numérico da inbox no Chatwoot
--   - chatwoot_inbox_name: nome da inbox (exibição/identificação)
--   - chatwoot_inbox_webhook_secret: secret do webhook da inbox (assinatura HMAC)
--   - chatwoot_inbox_identifier: inbox_identifier da inbox (Channel::Api)
ALTER TABLE evolution_instances
    ADD COLUMN chatwoot_inbox_id INTEGER,
    ADD COLUMN chatwoot_inbox_name VARCHAR(255),
    ADD COLUMN chatwoot_inbox_webhook_secret TEXT,
    ADD COLUMN chatwoot_inbox_identifier VARCHAR(255);
