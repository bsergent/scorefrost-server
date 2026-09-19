-- Migration 006: Two changes:
--   1. Rename score_type 'stars' -> 'chal_cum', updating all referencing score rows.
--   2. Update submit_solution_with_scores to ignore unknown score types (WARNING + skip).

-- Rename score_type 'stars' -> 'chal_cum'.
-- The FK on score.type_id has no ON UPDATE CASCADE, so we insert the new row,
-- migrate score data, then drop the old row.
INSERT INTO score_type (id, display_name, higher_is_better)
    VALUES ('chal_cum', 'Challenges Completed (Cumulative)', TRUE);
UPDATE score SET type_id = 'chal_cum' WHERE type_id = 'stars';
DELETE FROM score_type WHERE id = 'stars';

CREATE OR REPLACE FUNCTION submit_solution_with_scores(
    p_user_id UUID,
    p_level_id VARCHAR(16),
    p_level_version INTEGER,
    p_game_version VARCHAR(32),
    p_solution VARCHAR(1024),
    p_result SMALLINT,
    p_scores JSON
) RETURNS UUID AS $$
DECLARE
    v_solution_id UUID;
    v_score_record JSON;
    v_score_type VARCHAR(16);
    v_score_value INTEGER;
    v_score_type_exists BOOLEAN;
BEGIN
    INSERT INTO solution (user_id, level_id, level_version, game_version, solution, result)
    VALUES (p_user_id, p_level_id, p_level_version, p_game_version, p_solution, p_result)
    RETURNING id INTO v_solution_id;

    FOR v_score_record IN SELECT * FROM json_array_elements(p_scores)
    LOOP
        v_score_type := v_score_record->>'type';
        v_score_value := (v_score_record->>'value')::INTEGER;

        SELECT EXISTS(SELECT 1 FROM score_type WHERE id = v_score_type) INTO v_score_type_exists;

        IF NOT v_score_type_exists THEN
            -- Warn and skip; do not abort the transaction.
            RAISE WARNING 'Unknown score type ignored: % (solution_id=%)', v_score_type, v_solution_id;
            CONTINUE;
        END IF;

        INSERT INTO score (solution_id, type_id, score)
        VALUES (v_solution_id, v_score_type, v_score_value);
    END LOOP;

    RETURN v_solution_id;
END;
$$ LANGUAGE plpgsql;
