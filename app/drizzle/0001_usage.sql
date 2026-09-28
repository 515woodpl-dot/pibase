CREATE TABLE "project_usage" (
	"project_id" text PRIMARY KEY NOT NULL,
	"db_bytes" bigint DEFAULT 0 NOT NULL,
	"storage_bytes" bigint DEFAULT 0 NOT NULL,
	"users" integer DEFAULT 0 NOT NULL,
	"logs_until" timestamp,
	"block_all" boolean DEFAULT false NOT NULL,
	"block_writes" boolean DEFAULT false NOT NULL,
	"block_signups" boolean DEFAULT false NOT NULL,
	"checked_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
CREATE TABLE "usage_month" (
	"project_id" text NOT NULL,
	"month" text NOT NULL,
	"requests" integer DEFAULT 0 NOT NULL,
	CONSTRAINT "usage_month_project_id_month_pk" PRIMARY KEY("project_id","month")
);
--> statement-breakpoint
ALTER TABLE "project_usage" ADD CONSTRAINT "project_usage_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "usage_month" ADD CONSTRAINT "usage_month_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;