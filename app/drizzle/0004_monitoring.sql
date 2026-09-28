CREATE TABLE "alert_state" (
	"key" text PRIMARY KEY NOT NULL,
	"message" text NOT NULL,
	"since" timestamp DEFAULT now() NOT NULL,
	"resolved_at" timestamp
);
--> statement-breakpoint
-- Accounts created before email verification existed stay usable.
UPDATE "user" SET "email_verified" = true WHERE "email_verified" = false;
