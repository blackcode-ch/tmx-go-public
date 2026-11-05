// Package jobs runs bounded, cancellable daemon work away from control handlers.
// Adapters must authenticate the caller and authorize the input before submission;
// completing an audit never grants permission to place, release or execute content.
package jobs
