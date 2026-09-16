-- Revert get_best_scores(global) tie-break behavior.
-- This restores prior behavior that returned all rows tied at best_score.

CREATE OR REPLACE FUNCTION get_best_scores(
    p_user_id UUID,
    p_levels JSON,
    p_scope VARCHAR(16)
) RETURNS TABLE (
    level_id VARCHAR(16),
    level_version INTEGER,
    score_type VARCHAR(16),
    best_score INTEGER,
    user_id UUID,
    display_name VARCHAR(64),
    friend_code VARCHAR(9)
) AS $$
DECLARE
    v_level_record JSON;
    v_level_id VARCHAR(16);
    v_level_version INTEGER;
    v_sql TEXT;
    v_where_conditions TEXT[];
    v_final_where TEXT;
BEGIN
    IF p_levels IS NULL THEN
        -- No levels specified: match all levels at their latest version.
        v_final_where := 's.level_version = (SELECT MAX(s2.level_version) FROM solution s2 WHERE s2.level_id = s.level_id)';
    ELSE
        FOR v_level_record IN SELECT * FROM json_array_elements(p_levels)
        LOOP
            v_level_id := v_level_record->>'level_id';
            v_level_version := (v_level_record->>'level_version')::INTEGER;

            IF v_level_version = -1 THEN
                v_where_conditions := array_append(v_where_conditions,
                    format('(s.level_id = %L AND s.level_version = (
                        SELECT MAX(s2.level_version)
                        FROM solution s2
                        JOIN score sc2 ON s2.id = sc2.solution_id
                        WHERE s2.level_id = %L
                    ))', v_level_id, v_level_id));
            ELSE
                v_where_conditions := array_append(v_where_conditions,
                    format('(s.level_id = %L AND s.level_version = %s)', v_level_id, v_level_version));
            END IF;
        END LOOP;

        IF array_length(v_where_conditions, 1) = 0 THEN
            RAISE EXCEPTION 'No valid level specifications provided';
        END IF;

        v_final_where := array_to_string(v_where_conditions, ' OR ');
    END IF;

    CASE p_scope
        WHEN 'personal' THEN
            v_sql := format('
                SELECT
                    s.level_id,
                    s.level_version,
                    sc.type_id as score_type,
                    CASE
                        WHEN st.higher_is_better THEN MAX(sc.score)
                        ELSE MIN(sc.score)
                    END as best_score,
                    u.id as user_id,
                    COALESCE(u.display_name, '''') as display_name,
                    u.friend_code
                FROM solution s
                JOIN score sc ON s.id = sc.solution_id
                JOIN score_type st ON sc.type_id = st.id
                JOIN "user" u ON s.user_id = u.id
                WHERE s.user_id = %L AND (%s)
                GROUP BY s.level_id, s.level_version, sc.type_id, st.higher_is_better, u.id, u.display_name, u.friend_code
                ORDER BY s.level_id, s.level_version, sc.type_id
            ', p_user_id, v_final_where);

        WHEN 'friends' THEN
            RAISE EXCEPTION 'Friends scope not yet implemented';

        WHEN 'regional' THEN
            RAISE EXCEPTION 'Regional scope not yet implemented';

        WHEN 'global' THEN
            v_sql := format('
                WITH best_scores_cte AS (
                    SELECT
                        s.level_id,
                        s.level_version,
                        sc.type_id,
                        CASE
                            WHEN st.higher_is_better THEN MAX(sc.score)
                            ELSE MIN(sc.score)
                        END as best_score_value
                    FROM solution s
                    JOIN score sc ON s.id = sc.solution_id
                    JOIN score_type st ON sc.type_id = st.id
                    WHERE (%s)
                    GROUP BY s.level_id, s.level_version, sc.type_id, st.higher_is_better
                )
                SELECT DISTINCT
                    s.level_id,
                    s.level_version,
                    sc.type_id as score_type,
                    sc.score as best_score,
                    u.id as user_id,
                    COALESCE(u.display_name, '''') as display_name,
                    u.friend_code
                FROM solution s
                JOIN score sc ON s.id = sc.solution_id
                JOIN score_type st ON sc.type_id = st.id
                JOIN "user" u ON s.user_id = u.id
                JOIN best_scores_cte bsc ON (
                    s.level_id = bsc.level_id
                    AND s.level_version = bsc.level_version
                    AND sc.type_id = bsc.type_id
                    AND sc.score = bsc.best_score_value
                )
                WHERE (%s)
                ORDER BY s.level_id, s.level_version, sc.type_id
            ', v_final_where, v_final_where);

        ELSE
            RAISE EXCEPTION 'Invalid scope: %', p_scope;
    END CASE;

    RETURN QUERY EXECUTE v_sql;
END;
$$ LANGUAGE plpgsql;
