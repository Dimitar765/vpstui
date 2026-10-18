"use client";

// Projects/environments tree: create/rename/delete projects, add/delete
// environments, drill into an environment's apps.

import { useState } from "react";
import Link from "next/link";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post, put, del } from "@/lib/api";
import type { App, Environment, Project } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  ChevronDown,
  ChevronRight,
  FolderKanban,
  Pencil,
  Plus,
  Trash2,
} from "lucide-react";

interface ProjectRowProps {
  project: Project;
  apps: App[];
  onRenameClick: (p: Project) => void;
  onDeleted: () => void;
}

export default function ProjectsPage() {
  const qc = useQueryClient();
  const projects = useQuery({
    queryKey: ["projects"],
    queryFn: () => get<Project[]>("/api/projects"),
  });
  const apps = useQuery({ queryKey: ["apps"], queryFn: () => get<App[]>("/api/apps") });

  const [createOpen, setCreateOpen] = useState(false);
  const [nameDraft, setNameDraft] = useState("");
  const [renaming, setRenaming] = useState<Project | null>(null);

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["projects"] });
    qc.invalidateQueries({ queryKey: ["environments"] });
    qc.invalidateQueries({ queryKey: ["apps"] });
  };

  const createProject = async () => {
    if (!nameDraft.trim()) return;
    await post("/api/projects", { name: nameDraft.trim() });
    setCreateOpen(false);
    refresh();
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Projects</h1>
          <p className="text-sm text-muted-foreground">
            Group apps into projects and environments.
          </p>
        </div>
        <Button
          onClick={() => {
            setNameDraft("");
            setCreateOpen(true);
          }}
        >
          <Plus className="h-4 w-4" /> New project
        </Button>
      </div>

      {projects.isLoading ? (
        <p className="text-sm text-muted-foreground">Loading projects…</p>
      ) : (projects.data?.length ?? 0) === 0 ? (
        <p className="rounded-md border border-dashed p-8 text-center text-sm text-muted-foreground">
          No projects yet — create one to start deploying apps.
        </p>
      ) : (
        <div className="space-y-3">
          {projects.data!.map((p) => (
            <ProjectRow
              key={p.id}
              project={p}
              apps={apps.data ?? []}
              onRenameClick={(p) => {
                setNameDraft(p.name);
                setRenaming(p);
              }}
              onDeleted={refresh}
            />
          ))}
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>New project</DialogTitle>
          </DialogHeader>
          <Input
            autoFocus
            value={nameDraft}
            onChange={(e) => setNameDraft(e.target.value)}
            placeholder="project name"
            onKeyDown={(e) => {
              if (e.key === "Enter") createProject();
            }}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button disabled={!nameDraft.trim()} onClick={createProject}>
              Create
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={renaming !== null} onOpenChange={(v) => !v && setRenaming(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Rename project</DialogTitle>
          </DialogHeader>
          <Input
            autoFocus
            value={nameDraft}
            onChange={(e) => setNameDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && renaming && nameDraft.trim()) {
                put(`/api/projects/${renaming.id}`, { name: nameDraft.trim() }).then(
                  () => {
                    setRenaming(null);
                    refresh();
                  },
                );
              }
            }}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setRenaming(null)}>
              Cancel
            </Button>
            <Button
              disabled={!nameDraft.trim() || !renaming}
              onClick={async () => {
                if (!renaming) return;
                await put(`/api/projects/${renaming.id}`, { name: nameDraft.trim() });
                setRenaming(null);
                refresh();
              }}
            >
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function ProjectRow({ project, apps, onRenameClick, onDeleted }: ProjectRowProps) {
  const [open, setOpen] = useState(true);
  const [addingEnv, setAddingEnv] = useState(false);
  const [envName, setEnvName] = useState("");
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const qc = useQueryClient();

  const envs = useQuery({
    queryKey: ["environments", project.id],
    queryFn: () =>
      get<{ environments: Environment[] }>(`/api/projects/${project.id}`).then(
        (r) => r?.environments ?? [],
      ),
  });

  const refreshEnvs = () => {
    qc.invalidateQueries({ queryKey: ["environments", project.id] });
    qc.invalidateQueries({ queryKey: ["apps"] });
  };

  const addEnv = async () => {
    if (!envName.trim()) return;
    await post(`/api/projects/${project.id}/environments`, { name: envName.trim() });
    setAddingEnv(false);
    refreshEnvs();
  };

  const twoClickDelete = (armed: boolean, disarm: () => void, action: () => void) => (
    <Button
      variant="ghost"
      size="icon"
      title={armed ? "Click again to confirm" : "Delete"}
      onClick={() => {
        if (armed) action();
        else disarm();
      }}
    >
      <Trash2 className={`h-4 w-4 ${armed ? "text-destructive" : "text-destructive/60"}`} />
    </Button>
  );

  return (
    <div className="rounded-md border">
      <div className="flex items-center gap-2 p-3">
        <button onClick={() => setOpen(!open)} className="p-1" title={open ? "Collapse" : "Expand"}>
          {open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
        </button>
        <FolderKanban className="h-4 w-4 text-muted-foreground" />
        <span className="font-medium">{project.name}</span>
        <Badge variant="secondary">{envs.data?.length ?? 0} envs</Badge>
        <div className="ml-auto flex items-center gap-1">
          <Button
            variant="ghost"
            size="icon"
            title="Add environment"
            onClick={() => {
              setEnvName("");
              setAddingEnv(true);
              setOpen(true);
            }}
          >
            <Plus className="h-4 w-4" />
          </Button>
          <Button variant="ghost" size="icon" title="Rename project" onClick={() => onRenameClick(project)}>
            <Pencil className="h-4 w-4" />
          </Button>
          {twoClickDelete(confirmingDelete, () => setConfirmingDelete(true), () =>
            del(`/api/projects/${project.id}`).then(onDeleted),
          )}
        </div>
      </div>

      {open && (
        <div className="border-t px-3 py-2">
          {envs.isLoading && (
            <p className="py-1 text-sm text-muted-foreground">Loading environments…</p>
          )}
          {(envs.data?.length ?? 0) === 0 && !envs.isLoading && (
            <p className="py-1 text-sm text-muted-foreground">
              No environments — add one to deploy apps.
            </p>
          )}
          {envs.data?.map((env) => (
            <EnvLine
              key={env.id}
              env={env}
              apps={apps.filter((a) => a.environmentId === env.id)}
              onDeleted={refreshEnvs}
            />
          ))}
          {addingEnv && (
            <div className="flex items-center gap-2 py-2 pl-6">
              <Input
                autoFocus
                className="w-56"
                value={envName}
                placeholder="environment name (production, staging…)"
                onChange={(e) => setEnvName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") addEnv();
                }}
              />
              <Button size="sm" disabled={!envName.trim()} onClick={addEnv}>
                Add
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setAddingEnv(false)}>
                Cancel
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function EnvLine({
  env,
  apps,
  onDeleted,
}: {
  env: Environment;
  apps: App[];
  onDeleted: () => void;
}) {
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  return (
    <div className="flex items-center gap-2 py-1 pl-6">
      <Link
        href={`/projects/view?projectId=${env.id}`}
        className="text-sm hover:underline"
        title="Open environment"
      >
        {env.name}
      </Link>
      <Badge variant="outline">{apps.length} apps</Badge>
      <div className="ml-auto flex items-center gap-1">
        <Link
          href={`/apps/new?environmentId=${env.id}`}
          className="text-xs text-muted-foreground hover:text-foreground"
        >
          + app
        </Link>
        <Button
          variant="ghost"
          size="icon"
          title={confirmingDelete ? "Click again to confirm" : "Delete environment"}
          onClick={() => {
            if (confirmingDelete) {
              del(`/api/environments/${env.id}`).then(onDeleted);
            } else {
              setConfirmingDelete(true);
            }
          }}
        >
          <Trash2
            className={`h-3.5 w-3.5 ${confirmingDelete ? "text-destructive" : "text-destructive/60"}`}
          />
        </Button>
      </div>
    </div>
  );
}
