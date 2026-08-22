-- +goose Up

ALTER TABLE lotteries
    DROP CHECK chk_lotteries_participants,
    ADD CONSTRAINT chk_lotteries_participants CHECK (
        (draw_at IS NOT NULL OR participant_target IS NOT NULL)
        AND max_participants > 0
        AND participant_count <= max_participants
        AND (
            participant_target IS NULL
            OR (
                participant_target > 0
                AND participant_count <= participant_target
                AND participant_target <= max_participants
            )
        )
    );

-- +goose Down

ALTER TABLE lotteries
    DROP CHECK chk_lotteries_participants,
    ADD CONSTRAINT chk_lotteries_participants CHECK (
        participant_target IS NOT NULL
        AND participant_target > 0
        AND max_participants > 0
        AND participant_count <= participant_target
        AND participant_target <= max_participants
    );
