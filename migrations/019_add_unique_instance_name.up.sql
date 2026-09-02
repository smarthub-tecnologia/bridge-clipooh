-- FindByInstanceName (instance_repo.go) usa QueryRow assumindo no máximo uma
-- linha por instance_name — mas o schema nunca teve constraint que garantisse
-- isso (só o índice não-único de 008_add_performance_indexes). Fecha essa
-- fragilidade. Confirmado antes de criar esta migration: 0 duplicatas
-- existentes (GROUP BY instance_name HAVING COUNT(*) > 1).
ALTER TABLE evolution_instances ADD CONSTRAINT uq_evolution_instances_instance_name UNIQUE (instance_name);
