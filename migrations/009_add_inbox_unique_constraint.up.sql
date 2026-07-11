ALTER TABLE chatwoot_inboxes
  ADD CONSTRAINT chatwoot_inboxes_chatwoot_account_id_inbox_id_unique
  UNIQUE (chatwoot_account_id, inbox_id);
