-- Ensure get_best_scores(global) returns a single winner per level/version/score_type.
-- Tie-break rule: if multiple users share the best score, pick the earliest
-- submitted solution (solution.date_time_utc), then solution.id for deterministic order.

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
                WITH ranked_scores AS (
                    SELECT
                        s.level_id,
                        s.level_version,
                        sc.type_id as score_type,
                        sc.score as best_score,
                        u.id as user_id,
                        COALESCE(u.display_name, '''') as display_name,
                        u.friend_code,
                        ROW_NUMBER() OVER (
                            PARTITION BY s.level_id, s.level_version, sc.type_id
                            ORDER BY
                                CASE WHEN st.higher_is_better THEN sc.score END DESC,
                                CASE WHEN NOT st.higher_is_better THEN sc.score END ASC,
                                s.date_time_utc ASC,
                                s.id ASC
                        ) as score_rank
                    FROM solution s
                    JOIN score sc ON s.id = sc.solution_id
                    JOIN score_type st ON sc.type_id = st.id
                    JOIN "user" u ON s.user_id = u.id
                    WHERE (%s)
                )
                SELECT
                    rs.level_id,
                    rs.level_version,
                    rs.score_type,
                    rs.best_score,
                    rs.user_id,
                    rs.display_name,
                    rs.friend_code
                FROM ranked_scores rs
                WHERE rs.score_rank = 1
                ORDER BY rs.level_id, rs.level_version, rs.score_type
            ', v_final_where);

        ELSE
            RAISE EXCEPTION 'Invalid scope: %', p_scope;
    END CASE;

    RETURN QUERY EXECUTE v_sql;
END;
$$ LANGUAGE plpgsql;
