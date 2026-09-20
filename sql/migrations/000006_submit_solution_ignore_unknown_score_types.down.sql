-- Revert migration 006:
--   1. Restore submit_solution_with_scores to raise an exception for unknown score types.
--   2. Rename score_type 'chal_cum' back to 'stars'.

-- Revert score_type rename 'chal_cum' -> 'stars'.
INSERT INTO score_type (id, display_name, higher_is_better)
    VALUES ('stars', 'Stars', TRUE);
UPDATE score SET type_id = 'stars' WHERE type_id = 'chal_cum';
DELETE FROM score_type WHERE id = 'chal_cum';

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
            RAISE EXCEPTION 'Invalid score type: %', v_score_type;
        END IF;

        INSERT INTO score (solution_id, type_id, score)
        VALUES (v_solution_id, v_score_type, v_score_value);
    END LOOP;

    RETURN v_solution_id;
END;
$$ LANGUAGE plpgsql;
