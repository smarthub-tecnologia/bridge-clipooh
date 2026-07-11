-- Cache local de email -> chatwoot_user_id, preenchido sempre que CreateUser
-- tem sucesso na Platform API do Chatwoot.
--
-- Motivacao: a Platform API do Chatwoot nao tem endpoint pra buscar usuario
-- por e-mail. Quando o mesmo admin_email e reusado num provisionamento
-- seguinte (CreateUser falha com "ja existe"), esse cache e' a unica forma
-- do bridge descobrir o ID do usuario existente pra vincula-lo a conta nova
-- como agente/administrador — sem isso, a conta e a inbox sao criadas mas o
-- humano nunca fica com acesso a elas.
CREATE TABLE IF NOT EXISTS chatwoot_users (
    email            VARCHAR(255) PRIMARY KEY,
    chatwoot_user_id INTEGER NOT NULL,
    created_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    updated_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
);
