CREATE TABLE "usage_notice" (
	"project_id" text NOT NULL,
	"month" text NOT NULL,
	"metric" text NOT NULL,
	"level" integer NOT NULL,
	"sent_at" timestamp DEFAULT now() NOT NULL,
	CONSTRAINT "usage_notice_project_id_month_metric_level_pk" PRIMARY KEY("project_id","month","metric","level")
);
--> statement-breakpoint
ALTER TABLE "user" ADD COLUMN "suspended_at" timestamp;--> statement-breakpoint
ALTER TABLE "user" ADD COLUMN "deleted_at" timestamp;--> statement-breakpoint
ALTER TABLE "usage_notice" ADD CONSTRAINT "usage_notice_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;