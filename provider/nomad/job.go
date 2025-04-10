package nomad

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/hashicorp/nomad/api"
	"github.com/hashicorp/nomad/jobspec"
	"github.com/valkey-io/valkey-go"
)

// renderTemplate renders the template with the provided data
func renderTemplate(tmpl string, data any) (*bytes.Buffer, error) {
	tpl, err := template.New("").
		Delims("[[", "]]").
		Option("missingkey=error"). // Fails if a template tries to access a nonexistent key
		Parse(tmpl)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, &data); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	return &buf, nil
}

// registerJob renders and registers both the ACL and job on Nomad
func (n *Nomad) registerJob(jobID string, data *tplData) error {
	// Render job template
	jobBuf, err := renderTemplate(n.config.JobTemplate, data)
	if err != nil {
		return fmt.Errorf("failed to render job template: %w", err)
	}

	// Parse the job specification
	job, err := jobspec.Parse(jobBuf)
	if err != nil {
		return fmt.Errorf("failed to parse job spec: %w", err)
	}

	job.ID = &data.JobID
	job.Namespace = &data.Namespace

	// Register ACL policy for the job
	policyName, err := n.upsertACL(*job.ID, *job.Namespace, data)
	if err != nil {
		return fmt.Errorf("failed to upsert ACL: %w", err)
	}

	// Register the job on Nomad
	_, _, err = n.client.Jobs().Register(job, nil)
	if err != nil {
		// Try to clean up the ACL if job registration fails
		if cleanErr := n.deleteACL(policyName, *job.Namespace); cleanErr != nil {
			n.logger.Warn("failed to clean up ACL after job registration failure",
				"job", *job.ID, "policy", policyName, "error", cleanErr)
		}
		return fmt.Errorf("failed to register job: %w", err)
	}

	// Store job details in cache for later deregistration
	cacheKey := fmt.Sprintf(cacheKeyFormat, n.config.Name, jobID, data.Namespace)

	// Get the raw valkey client
	rawClient := n.cache.RawClient()

	cmd := rawClient.B().
		Hset().Key(cacheKey).FieldValue().
		FieldValue("jobID", *job.ID).
		FieldValue("namespace", *job.Namespace).
		FieldValue("acl", policyName).
		Build()

	result := rawClient.Do(n.ctx, cmd)
	if err := result.Error(); err != nil {
		n.logger.Error("failed to add job to cache", "job", *job.ID, "namespace", *job.Namespace, "error", err)
		// Continue execution even if cache update fails - the job is already registered
	}

	n.logger.Info("registered job", "job", *job.ID, "namespace", *job.Namespace)
	return nil
}

// deregisterJob removes a job from Nomad
func (n *Nomad) deregisterJob(jobID string, namespace string) error {
	// Skip if deregistration is disabled
	if !n.config.Deregister {
		return nil
	}

	// Fetch job details from cache
	cacheKey := fmt.Sprintf(cacheKeyFormat, n.config.Name, jobID, namespace)
	rawClient := n.cache.RawClient()

	cmd := rawClient.B().
		Hmget().Key(cacheKey).
		Field("jobID").
		Field("namespace").
		Field("acl").
		Build()

	result := rawClient.Do(n.ctx, cmd)
	val, err := result.AsStrSlice()

	if err != nil {
		if err == valkey.Nil {
			n.logger.Info("no such job in cache", "job", jobID, "namespace", namespace)
			return nil // Not an error, job wasn't in cache
		}
		n.logger.Error("failed to fetch job from cache",
			"job", jobID,
			"namespace", namespace,
			"error", err)
		// Continue with deregistration attempt even if cache fails
		// This allows cleanup of Nomad jobs even if cache is having issues
		_, _, err = n.client.Jobs().Deregister(jobID, false, &api.WriteOptions{Namespace: namespace})
		if err != nil {
			return fmt.Errorf("failed to deregister job %s in namespace %s: %w", jobID, namespace, err)
		}
		return nil
	}

	// Check if we got valid data back (key exists and has values)
	if len(val) < 3 || val[0] == "" || val[1] == "" {
		n.logger.Info("no such job in cache", "job", jobID, "namespace", namespace)
		return nil // Not an error, job wasn't in cache or had incomplete data
	}

	// Extract job details
	id, ns, acl := val[0], val[1], val[2]

	// Deregister job
	_, _, err = n.client.Jobs().Deregister(id, false, &api.WriteOptions{Namespace: ns})
	if err != nil {
		return fmt.Errorf("failed to deregister job %s in namespace %s: %w", id, ns, err)
	}

	// Attempt to clean up ACL (don't fail if we can't)
	if acl != "" {
		if err := n.deleteACL(acl, ns); err != nil {
			n.logger.Warn("failed to delete ACL policy, continuing",
				"job", id, "namespace", ns, "policy", acl, "error", err)
		}
	}

	// Remove job from cache
	delCmd := rawClient.B().
		Hdel().Key(cacheKey).
		Field("jobID").
		Field("namespace").
		Field("acl").
		Build()

	if err := rawClient.Do(n.ctx, delCmd).Error(); err != nil {
		n.logger.Warn("failed to remove job from cache, continuing",
			"job", id, "namespace", ns, "error", err)
	}

	n.logger.Info("successfully deregistered job", "job", id, "namespace", ns)
	return nil
}

// upsertACL creates or updates the ACL policy for a job
func (n *Nomad) upsertACL(id string, ns string, data *tplData) (string, error) {
	// Render ACL template
	rulesBuf, err := renderTemplate(n.config.AclTemplate, data)
	if err != nil {
		return "", fmt.Errorf("failed to render ACL template: %w", err)
	}

	// Create policy name with a consistent pattern
	policyName := id + "-access"

	// Create ACL policy
	policy := &api.ACLPolicy{
		Name:        policyName,
		Description: fmt.Sprintf("ACL policy for %s. Generated by damon for job %s.", id, data.JobID),
		Rules:       rulesBuf.String(),
		JobACL: &api.JobACL{
			Namespace: ns,
			JobID:     id,
		},
	}

	n.logger.Info("creating ACL policy", "job", id, "namespace", ns, "policy", policyName)
	_, err = n.client.ACLPolicies().Upsert(policy, &api.WriteOptions{Namespace: ns})
	if err != nil {
		return "", fmt.Errorf("failed to upsert ACL policy: %w", err)
	}

	return policyName, nil
}

// deleteACL removes an ACL policy
func (n *Nomad) deleteACL(policy string, ns string) error {
	if policy == "" {
		return nil // Nothing to delete
	}

	n.logger.Info("deleting ACL policy", "policy", policy, "namespace", ns)
	err := n.client.ACLPolicies().Delete(policy, &api.WriteOptions{Namespace: ns})
	if err != nil {
		return fmt.Errorf("failed to delete ACL policy %s: %w", policy, err)
	}
	return nil
}
